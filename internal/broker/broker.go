package broker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/michaelquigley/mercurius/internal/prompt"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/roundlog"
	"github.com/michaelquigley/mercurius/internal/schema"
)

const (
	defaultBudget = 4
	stateActive   = "active"
	stateClosed   = "closed"
)

var safeArtifactName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Broker orchestrates in-memory sessions and review rounds.
type Broker struct {
	mu        sync.Mutex
	options   Options
	reviewers map[string]ReviewerSpec
	sessions  map[string]*session
}

type session struct {
	id              string
	dir             string
	state           string
	verdict         *string
	openedAt        time.Time
	closedAt        *time.Time
	budget          int
	artifacts       []Artifact
	reviewerName    string
	reviewer        reviewer.Reviewer
	rounds          []*round
	promptOverrides string
}

type round struct {
	number    int
	openedAt  time.Time
	logPath   string
	hasNotes  bool
	manifest  []ArtifactManifestEntry
	results   []ReviewerResult
	refs      map[string]struct{}
	decisions []Decision
}

type snapshotResult struct {
	manifest          []ArtifactManifestEntry
	promptArtifacts   []prompt.Artifact
	reviewerArtifacts []reviewer.Artifact
	snapshotDir       string
}

// New creates an in-memory broker.
func New(options Options) *Broker {
	if options.DefaultBudget == 0 {
		options.DefaultBudget = defaultBudget
	}
	reviewers := make(map[string]ReviewerSpec, len(options.Reviewers))
	for _, spec := range options.Reviewers {
		reviewers[spec.Name] = spec
	}
	return &Broker{
		options:   options,
		reviewers: reviewers,
		sessions:  map[string]*session{},
	}
}

// OpenSession validates artifacts and creates a session directory.
func (b *Broker) OpenSession(ctx context.Context, req OpenSessionRequest) (OpenSessionResponse, error) {
	if err := ctx.Err(); err != nil {
		return OpenSessionResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	budget := req.Budget
	if budget == 0 {
		budget = b.options.DefaultBudget
	}
	if budget <= 0 {
		return OpenSessionResponse{}, brokerError(CodeInvalidBudget, "budget must be greater than zero", nil, map[string]any{"budget": req.Budget})
	}

	artifacts, err := validateArtifacts(req.Artifacts)
	if err != nil {
		return OpenSessionResponse{}, brokerError(CodeInvalidArtifacts, "invalid artifacts", err, nil)
	}

	spec, err := b.resolveReviewer(req.Reviewers)
	if err != nil {
		return OpenSessionResponse{}, err
	}

	if err := ensureLogDestination(b.options.LogDestination); err != nil {
		return OpenSessionResponse{}, brokerError(CodeInvalidLogDestination, "invalid log destination", err, nil)
	}

	id, dir, err := b.createSessionDir()
	if err != nil {
		return OpenSessionResponse{}, brokerError(CodeInternalError, "create session directory", err, nil)
	}

	reviewerImpl := spec.Factory(dir)
	if reviewerImpl == nil {
		return OpenSessionResponse{}, brokerError(CodeInternalError, "reviewer factory returned nil", nil, map[string]any{"reviewer": spec.Name})
	}

	openedAt := time.Now().UTC()
	b.sessions[id] = &session{
		id:              id,
		dir:             dir,
		state:           stateActive,
		openedAt:        openedAt,
		budget:          budget,
		artifacts:       artifacts,
		reviewerName:    spec.Name,
		reviewer:        reviewerImpl,
		promptOverrides: b.options.PromptOverrides,
	}

	return OpenSessionResponse{
		SessionID:  id,
		SessionDir: dir,
		OpenedAt:   openedAt,
		Budget:     budget,
	}, nil
}

// ReviewRound snapshots artifacts, dispatches the reviewer, validates, and logs.
func (b *Broker) ReviewRound(ctx context.Context, req ReviewRoundRequest) (ReviewRoundResponse, error) {
	if err := ctx.Err(); err != nil {
		return ReviewRoundResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(req.SessionID)
	if err != nil {
		return ReviewRoundResponse{}, err
	}
	if s.state == stateClosed {
		return ReviewRoundResponse{}, brokerError(CodeSessionClosed, "session is closed", nil, map[string]any{"session_id": req.SessionID})
	}
	if len(s.rounds) >= s.budget {
		return ReviewRoundResponse{}, brokerError(CodeBudgetExhausted, "session budget is exhausted", nil, map[string]any{"session_id": req.SessionID, "budget": s.budget})
	}

	artifacts := cloneArtifacts(s.artifacts)
	hasOverride := req.Artifacts != nil
	if hasOverride {
		validated, err := validateArtifacts(req.Artifacts)
		if err != nil {
			return ReviewRoundResponse{}, brokerError(CodeInvalidArtifacts, "invalid artifact override", err, nil)
		}
		artifacts = validated
	}

	roundNumber := len(s.rounds) + 1
	snapshots, err := snapshotArtifacts(s.dir, roundNumber, artifacts)
	if err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		return ReviewRoundResponse{}, brokerError(CodeInvalidArtifacts, "snapshot artifacts", err, nil)
	}

	promptText, schemaBytes := prompt.Build(prompt.Request{
		Artifacts:       snapshots.promptArtifacts,
		PriorDecisions:  s.priorDecisions(),
		PromptOverrides: s.promptOverrides,
	})

	resp, err := s.reviewer.Review(ctx, reviewer.ReviewRequest{
		Prompt:    promptText,
		Artifacts: snapshots.reviewerArtifacts,
		Schema:    schemaBytes,
		SessionMeta: reviewer.SessionMetadata{
			SessionID:      s.id,
			RoundNumber:    roundNumber,
			PriorDecisions: s.priorDecisions(),
		},
	})
	if err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		return ReviewRoundResponse{}, brokerError(CodeReviewerFailed, "reviewer failed", err, map[string]any{"reviewer": s.reviewerName})
	}

	output, err := schema.ParseReviewOutput(resp.Raw)
	if err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		return ReviewRoundResponse{}, brokerError(CodeSchemaViolation, "reviewer output failed schema validation", err, map[string]any{
			"reviewer": s.reviewerName,
			"raw":      string(resp.Raw),
		})
	}

	openedAt := time.Now().UTC()
	logPath := filepath.Join(s.dir, fmt.Sprintf("round-%02d.md", roundNumber))
	results := []ReviewerResult{{
		ReviewerName: s.reviewerName,
		Raw:          append(json.RawMessage(nil), resp.Raw...),
		UsageNotes:   resp.UsageNotes,
	}}
	if err := roundlog.WriteInitial(logPath, roundlog.Entry{
		SessionID:   s.id,
		RoundNumber: roundNumber,
		OpenedAt:    openedAt,
		Verdict:     output.Verdict,
		Manifest:    toRoundlogManifest(snapshots.manifest),
		Reviewers:   toRoundlogReviewers(results),
	}); err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		return ReviewRoundResponse{}, brokerError(CodeInternalError, "write round log", err, map[string]any{"log_path": logPath})
	}

	if hasOverride {
		s.artifacts = artifacts
	}
	round := &round{
		number:   roundNumber,
		openedAt: openedAt,
		logPath:  logPath,
		manifest: cloneManifest(snapshots.manifest),
		results:  cloneResults(results),
		refs:     refsFromOutput(output),
	}
	s.rounds = append(s.rounds, round)

	return ReviewRoundResponse{
		RoundNumber: roundNumber,
		LogPath:     logPath,
		Manifest:    cloneManifest(snapshots.manifest),
		Reviewers:   cloneResults(results),
	}, nil
}

// RecordRoundNotes replaces a round's commentary and decisions.
func (b *Broker) RecordRoundNotes(ctx context.Context, req RecordRoundNotesRequest) (RecordRoundNotesResponse, error) {
	if err := ctx.Err(); err != nil {
		return RecordRoundNotesResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(req.SessionID)
	if err != nil {
		return RecordRoundNotesResponse{}, err
	}
	round := s.findRound(req.RoundNumber)
	if round == nil {
		return RecordRoundNotesResponse{}, brokerError(CodeUnknownRound, "round not found", nil, map[string]any{"round_number": req.RoundNumber})
	}
	if strings.TrimSpace(req.Commentary) == "" && len(req.Decisions) == 0 {
		return RecordRoundNotesResponse{}, brokerError(CodeEmptyNotes, "commentary and decisions are empty", nil, nil)
	}

	decisions := cloneDecisions(req.Decisions)
	for _, decision := range decisions {
		if _, ok := round.refs[decision.Ref]; !ok {
			return RecordRoundNotesResponse{}, brokerError(CodeUnknownRef, "decision ref is unknown", nil, map[string]any{"ref": decision.Ref})
		}
		if !validDisposition(decision.Disposition) {
			return RecordRoundNotesResponse{}, brokerError(CodeInvalidDecision, "decision disposition is invalid", nil, map[string]any{"disposition": decision.Disposition})
		}
	}

	if err := roundlog.UpdateNotes(round.logPath, req.Commentary, toRoundlogDecisions(decisions)); err != nil {
		return RecordRoundNotesResponse{}, brokerError(CodeInternalError, "update round notes", err, map[string]any{"log_path": round.logPath})
	}
	round.hasNotes = true
	round.decisions = decisions

	return RecordRoundNotesResponse{
		RoundNumber:        req.RoundNumber,
		LogPath:            round.logPath,
		CommentaryRecorded: strings.TrimSpace(req.Commentary) != "",
		DecisionsRecorded:  len(decisions),
	}, nil
}

// CloseSession marks a session closed.
func (b *Broker) CloseSession(ctx context.Context, req CloseSessionRequest) (CloseSessionResponse, error) {
	if err := ctx.Err(); err != nil {
		return CloseSessionResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(req.SessionID)
	if err != nil {
		return CloseSessionResponse{}, err
	}
	if s.state == stateClosed {
		return CloseSessionResponse{}, brokerError(CodeAlreadyClosed, "session is already closed", nil, map[string]any{"session_id": req.SessionID})
	}
	if !validCloseVerdict(req.Verdict) {
		return CloseSessionResponse{}, brokerError(CodeInvalidVerdict, "session verdict is invalid", nil, map[string]any{"verdict": req.Verdict})
	}

	closedAt := time.Now().UTC()
	s.state = stateClosed
	s.verdict = &req.Verdict
	s.closedAt = &closedAt

	return CloseSessionResponse{
		SessionID: s.id,
		Verdict:   req.Verdict,
		ClosedAt:  closedAt,
	}, nil
}

// SessionStatus returns a read-only session view.
func (b *Broker) SessionStatus(ctx context.Context, sessionID string) (SessionStatusResponse, error) {
	if err := ctx.Err(); err != nil {
		return SessionStatusResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(sessionID)
	if err != nil {
		return SessionStatusResponse{}, err
	}
	return s.status(), nil
}

// ListSessions returns all sessions known to this broker.
func (b *Broker) ListSessions(ctx context.Context) (ListSessionsResponse, error) {
	if err := ctx.Err(); err != nil {
		return ListSessionsResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	summaries := make([]SessionSummary, 0, len(b.sessions))
	for _, s := range b.sessions {
		summaries = append(summaries, SessionSummary{
			SessionID:  s.id,
			State:      s.state,
			Verdict:    cloneStringPtr(s.verdict),
			OpenedAt:   s.openedAt,
			RoundsUsed: len(s.rounds),
		})
	}
	slices.SortFunc(summaries, func(a, b SessionSummary) int {
		return strings.Compare(a.SessionID, b.SessionID)
	})
	return ListSessionsResponse{Sessions: summaries}, nil
}

func (b *Broker) session(id string) (*session, error) {
	s, ok := b.sessions[id]
	if !ok {
		return nil, brokerError(CodeUnknownSession, "session not found", nil, map[string]any{"session_id": id})
	}
	return s, nil
}

func (b *Broker) resolveReviewer(names []string) (ReviewerSpec, error) {
	if len(names) == 0 {
		if len(b.reviewers) != 1 {
			return ReviewerSpec{}, brokerError("panel_mode_unsupported", "exactly one reviewer must be selected", nil, nil)
		}
		for _, spec := range b.reviewers {
			return spec, nil
		}
	}
	if len(names) != 1 {
		return ReviewerSpec{}, brokerError("panel_mode_unsupported", "v1 supports exactly one reviewer", nil, map[string]any{"reviewers": names})
	}
	spec, ok := b.reviewers[names[0]]
	if !ok {
		return ReviewerSpec{}, brokerError("unknown_reviewer", "reviewer is not configured", nil, map[string]any{"reviewer": names[0]})
	}
	if spec.Factory == nil {
		return ReviewerSpec{}, brokerError(CodeInternalError, "reviewer factory is nil", nil, map[string]any{"reviewer": names[0]})
	}
	return spec, nil
}

func (b *Broker) createSessionDir() (string, string, error) {
	for range 8 {
		id, err := newSessionID()
		if err != nil {
			return "", "", err
		}
		dir := filepath.Join(b.options.LogDestination, id)
		if err := os.Mkdir(dir, 0o700); err == nil {
			return id, dir, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", "", err
		}
	}
	return "", "", errors.New("could not allocate unique session id")
}

func (s *session) findRound(roundNumber int) *round {
	for _, round := range s.rounds {
		if round.number == roundNumber {
			return round
		}
	}
	return nil
}

func (s *session) priorDecisions() []reviewer.PriorDecision {
	var decisions []reviewer.PriorDecision
	for _, round := range s.rounds {
		for _, decision := range round.decisions {
			decisions = append(decisions, reviewer.PriorDecision{
				RoundNumber: round.number,
				Ref:         decision.Ref,
				Disposition: decision.Disposition,
				Note:        decision.Note,
			})
		}
	}
	return decisions
}

func (s *session) status() SessionStatusResponse {
	rounds := make([]RoundStatus, 0, len(s.rounds))
	for _, round := range s.rounds {
		rounds = append(rounds, RoundStatus{
			RoundNumber:   round.number,
			OpenedAt:      round.openedAt,
			LogPath:       round.logPath,
			HasNotes:      round.hasNotes,
			DecisionCount: len(round.decisions),
		})
	}
	return SessionStatusResponse{
		SessionID:  s.id,
		State:      s.state,
		Verdict:    cloneStringPtr(s.verdict),
		OpenedAt:   s.openedAt,
		ClosedAt:   cloneTimePtr(s.closedAt),
		Budget:     s.budget,
		RoundsUsed: len(s.rounds),
		Rounds:     rounds,
	}
}

func validateArtifacts(artifacts []Artifact) ([]Artifact, error) {
	if len(artifacts) == 0 {
		return nil, errors.New("artifact list is empty")
	}
	seen := map[string]struct{}{}
	validated := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if err := validateArtifactName(artifact.Name); err != nil {
			return nil, err
		}
		if _, ok := seen[artifact.Name]; ok {
			return nil, fmt.Errorf("duplicate artifact name '%s'", artifact.Name)
		}
		seen[artifact.Name] = struct{}{}

		inline := artifact.Content != nil
		if !inline {
			if !filepath.IsAbs(artifact.Path) {
				return nil, fmt.Errorf("artifact '%s' path is not absolute", artifact.Name)
			}
			if _, err := os.ReadFile(artifact.Path); err != nil {
				return nil, fmt.Errorf("artifact '%s' is not readable: %w", artifact.Name, err)
			}
		}
		artifact.Content = append([]byte(nil), artifact.Content...)
		validated = append(validated, artifact)
	}
	return validated, nil
}

func validateArtifactName(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return fmt.Errorf("artifact name '%s' length is outside 1-64", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("artifact name '%s' is not allowed", name)
	}
	if !safeArtifactName.MatchString(name) {
		return fmt.Errorf("artifact name '%s' is unsafe", name)
	}
	return nil
}

func snapshotArtifacts(sessionDir string, roundNumber int, artifacts []Artifact) (snapshotResult, error) {
	snapshotDir := filepath.Join(sessionDir, "snapshots", fmt.Sprintf("round-%02d", roundNumber))
	result := snapshotResult{snapshotDir: snapshotDir}
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		return result, err
	}

	for _, artifact := range artifacts {
		inline := artifact.Content != nil
		content := artifact.Content
		if !inline {
			raw, err := os.ReadFile(artifact.Path)
			if err != nil {
				return result, err
			}
			content = raw
		}
		snapshotPath := filepath.Join(snapshotDir, artifact.Name)
		if err := os.WriteFile(snapshotPath, content, 0o600); err != nil {
			return result, err
		}
		hash := sha256.Sum256(content)
		hashText := "sha256:" + hex.EncodeToString(hash[:])
		manifest := ArtifactManifestEntry{
			Name:         artifact.Name,
			SourcePath:   artifact.Path,
			SnapshotPath: snapshotPath,
			Size:         int64(len(content)),
			Hash:         hashText,
			Inline:       inline,
		}
		if inline {
			manifest.SourcePath = ""
		}
		result.manifest = append(result.manifest, manifest)
		result.promptArtifacts = append(result.promptArtifacts, prompt.Artifact{
			Name:         artifact.Name,
			SourcePath:   artifact.Path,
			SnapshotPath: snapshotPath,
			Hash:         hashText,
			Content:      append([]byte(nil), content...),
			Inline:       inline,
		})
		result.reviewerArtifacts = append(result.reviewerArtifacts, reviewer.Artifact{
			Name: artifact.Name,
			Path: snapshotPath,
		})
	}
	return result, nil
}

func cleanupSnapshot(snapshotDir string) {
	if snapshotDir != "" {
		_ = os.RemoveAll(snapshotDir)
	}
}

func ensureLogDestination(path string) error {
	if path == "" {
		return errors.New("log destination is empty")
	}
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("log destination '%s' is not a directory", path)
		}
		return checkWritable(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if info, err := os.Stat(parent); err != nil {
		return fmt.Errorf("log destination parent '%s' is not available: %w", parent, err)
	} else if !info.IsDir() {
		return fmt.Errorf("log destination parent '%s' is not a directory", parent)
	}
	if err := checkWritable(parent); err != nil {
		return err
	}
	return os.Mkdir(path, 0o700)
}

func checkWritable(dir string) error {
	file, err := os.CreateTemp(dir, ".mercurius-write-*")
	if err != nil {
		return fmt.Errorf("directory '%s' is not writable: %w", dir, err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func validDisposition(disposition string) bool {
	return disposition == "accepted" || disposition == "rejected" || disposition == "deferred"
}

func validCloseVerdict(verdict string) bool {
	return verdict == "ready_to_build" || verdict == "paused" || verdict == "abandoned"
}

func refsFromOutput(output schema.ReviewOutput) map[string]struct{} {
	refs := map[string]struct{}{}
	for _, concern := range output.Concerns {
		refs[concern.ID] = struct{}{}
	}
	for _, question := range output.Questions {
		refs[question.ID] = struct{}{}
	}
	return refs
}

func newSessionID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var b strings.Builder
	b.WriteString("s_")
	for range 12 {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}

func cloneArtifacts(artifacts []Artifact) []Artifact {
	out := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		artifact.Content = append([]byte(nil), artifact.Content...)
		out = append(out, artifact)
	}
	return out
}

func cloneManifest(manifest []ArtifactManifestEntry) []ArtifactManifestEntry {
	return append([]ArtifactManifestEntry(nil), manifest...)
}

func cloneResults(results []ReviewerResult) []ReviewerResult {
	out := make([]ReviewerResult, 0, len(results))
	for _, result := range results {
		result.Raw = append(json.RawMessage(nil), result.Raw...)
		out = append(out, result)
	}
	return out
}

func cloneDecisions(decisions []Decision) []Decision {
	return append([]Decision(nil), decisions...)
}

func cloneStringPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func toRoundlogManifest(manifest []ArtifactManifestEntry) []roundlog.ArtifactManifestEntry {
	out := make([]roundlog.ArtifactManifestEntry, 0, len(manifest))
	for _, artifact := range manifest {
		out = append(out, roundlog.ArtifactManifestEntry{
			Name:         artifact.Name,
			SourcePath:   artifact.SourcePath,
			SnapshotPath: artifact.SnapshotPath,
			Size:         artifact.Size,
			Hash:         artifact.Hash,
			Inline:       artifact.Inline,
		})
	}
	return out
}

func toRoundlogReviewers(results []ReviewerResult) []roundlog.ReviewerOutput {
	out := make([]roundlog.ReviewerOutput, 0, len(results))
	for _, result := range results {
		out = append(out, roundlog.ReviewerOutput{
			Name:       result.ReviewerName,
			Raw:        result.Raw,
			UsageNotes: result.UsageNotes,
		})
	}
	return out
}

func toRoundlogDecisions(decisions []Decision) []roundlog.Decision {
	out := make([]roundlog.Decision, 0, len(decisions))
	for _, decision := range decisions {
		out = append(out, roundlog.Decision{
			Ref:         decision.Ref,
			Disposition: decision.Disposition,
			Note:        decision.Note,
		})
	}
	return out
}

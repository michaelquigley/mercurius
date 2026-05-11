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
	"strings"
	"sync"
	"time"

	"github.com/michaelquigley/mercurius/internal/monitor"
	"github.com/michaelquigley/mercurius/internal/prompt"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/roundlog"
	"github.com/michaelquigley/mercurius/internal/schema"
)

const (
	defaultMaxFindings  = 6
	stateActive         = "active"
	stateClosed         = "closed"
	roundStateRunning   = "running"
	roundStateCompleted = "completed"
	roundStateFailed    = "failed"
	roundLogName        = "_round.md"
	roundPromptName     = "_prompt.md"
	roundNotesName      = "_notes.md"
)

var safeArtifactName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Broker orchestrates in-memory sessions and review rounds.
type Broker struct {
	mu       sync.Mutex
	options  Options
	sessions map[string]*session
}

type session struct {
	id           string
	dir          string
	state        string
	openedAt     time.Time
	closedAt     *time.Time
	rounds       []*round
	lastError    *ErrorInfo
	activeJob    *roundJob
	lastRoundJob *roundJob
}

type round struct {
	number     int
	openedAt   time.Time
	logPath    string
	notesPath  string
	hasNotes   bool
	manifest   []ArtifactManifestEntry
	results    []ReviewerResult
	refs       map[string]refKind
	decisions  []Decision
	commentary string
}

// refKind records whether a reviewer-emitted id came from concerns, questions,
// or advisory_notes.
type refKind string

const (
	refKindConcern  refKind = "concern"
	refKindQuestion refKind = "question"
	refKindAdvisory refKind = "advisory"
)

type snapshotResult struct {
	manifest          []ArtifactManifestEntry
	promptArtifacts   []prompt.Artifact
	reviewerArtifacts []reviewer.Artifact
	roundDir          string
}

type roundJob struct {
	sessionID     string
	sessionDir    string
	roundNumber   int
	state         string
	reviewer      reviewer.Reviewer
	reviewerName  string
	artifacts     []Artifact
	reviewContext string
	reviewFocus   string
	maxFindings   int
	startedAt     time.Time
	updatedAt     time.Time
	completedAt   *time.Time
	logPath       string
	statusPath    string
	result        CollectedRoundResponse
	err           error
	errorInfo     *ErrorInfo
	done          chan struct{}
}

// New creates an in-memory broker.
func New(options Options) *Broker {
	if options.MaxFindings <= 0 {
		options.MaxFindings = defaultMaxFindings
	}
	return &Broker{
		options:  options,
		sessions: map[string]*session{},
	}
}

// OpenSession creates a session directory and binds the configured reviewer.
func (b *Broker) OpenSession(ctx context.Context, _ OpenSessionRequest) (OpenSessionResponse, error) {
	if err := ctx.Err(); err != nil {
		return OpenSessionResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.options.Reviewer == nil {
		return OpenSessionResponse{}, brokerError(CodeInternalError, "no reviewer configured", nil, nil)
	}
	if err := ensureLogDestination(b.options.LogDestination); err != nil {
		return OpenSessionResponse{}, brokerError(CodeUserError, "invalid log destination", err, nil)
	}

	id, dir, err := b.createSessionDir()
	if err != nil {
		return OpenSessionResponse{}, brokerError(CodeInternalError, "create session directory", err, nil)
	}

	openedAt := time.Now().UTC()
	s := &session{
		id:       id,
		dir:      dir,
		state:    stateActive,
		openedAt: openedAt,
	}
	b.sessions[id] = s

	if err := b.persistSessionLocked(s); err != nil {
		delete(b.sessions, id)
		_ = os.RemoveAll(dir)
		return OpenSessionResponse{}, brokerError(CodeInternalError, "write session status", err, nil)
	}

	return OpenSessionResponse{
		SessionID:            id,
		SessionDir:           dir,
		OpenedAt:             openedAt,
		MaxFindings:          b.options.MaxFindings,
		ReviewContextPresent: strings.TrimSpace(b.options.ReviewContext) != "",
		ReviewFocusPresent:   strings.TrimSpace(b.options.ReviewFocus) != "",
		Reviewer:             b.options.ReviewerInfo,
	}, nil
}

// StartReviewRound starts a background review round for the named session.
func (b *Broker) StartReviewRound(ctx context.Context, req StartRoundRequest) (StartReviewRoundResponse, error) {
	if err := ctx.Err(); err != nil {
		return StartReviewRoundResponse{}, err
	}

	b.mu.Lock()

	s, err := b.session(req.SessionID)
	if err != nil {
		b.mu.Unlock()
		return StartReviewRoundResponse{}, err
	}
	if s.state == stateClosed {
		err := brokerError(CodeConflict, "session is closed", nil, map[string]any{"session_id": req.SessionID})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		b.mu.Unlock()
		return StartReviewRoundResponse{}, err
	}
	if s.activeJob != nil {
		err := brokerError(CodeConflict, "a review round is already running", nil, map[string]any{
			"session_id":   req.SessionID,
			"round_number": s.activeJob.roundNumber,
		})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		b.mu.Unlock()
		return StartReviewRoundResponse{}, err
	}

	artifacts, err := validateArtifacts(req.Artifacts)
	if err != nil {
		err := brokerError(CodeUserError, "invalid artifacts", err, nil)
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		b.mu.Unlock()
		return StartReviewRoundResponse{}, err
	}

	roundNumber := len(s.rounds) + 1
	startedAt := time.Now().UTC()
	job := &roundJob{
		sessionID:     s.id,
		sessionDir:    s.dir,
		roundNumber:   roundNumber,
		state:         roundStateRunning,
		reviewer:      b.options.Reviewer,
		reviewerName:  b.options.ReviewerInfo.Name,
		artifacts:     artifacts,
		reviewContext: strings.TrimSpace(b.options.ReviewContext),
		reviewFocus:   strings.TrimSpace(b.options.ReviewFocus),
		maxFindings:   b.options.MaxFindings,
		startedAt:     startedAt,
		updatedAt:     startedAt,
		statusPath:    monitor.StatusPath(s.dir),
		done:          make(chan struct{}),
	}
	s.activeJob = job
	s.lastRoundJob = job
	s.lastError = nil
	if err := b.persistSessionLocked(s); err != nil {
		s.activeJob = nil
		s.lastRoundJob = nil
		s.recordError(brokerError(CodeInternalError, "write session status", err, nil))
		_ = b.persistSessionLocked(s)
		b.mu.Unlock()
		return StartReviewRoundResponse{}, brokerError(CodeInternalError, "write session status", err, nil)
	}
	response := StartReviewRoundResponse{
		SessionID:      s.id,
		RoundNumber:    roundNumber,
		State:          roundStateRunning,
		Reviewer:       b.options.ReviewerInfo.Name,
		StartedAt:      startedAt,
		StatusPath:     job.statusPath,
		EventsPath:     "",
		MonitorCommand: b.monitorCommand(s.id),
		NextAction:     "tell the user this review is running; they can monitor it with the monitor_command and re-engage you when the round completes",
	}
	b.mu.Unlock()

	go b.executeRoundJob(job)
	return response, nil
}

func (b *Broker) executeRoundJob(job *roundJob) {
	b.touchRoundStatus(job)

	snapshots, err := snapshotArtifacts(job.sessionDir, job.roundNumber, job.artifacts)
	if err != nil {
		cleanupRoundDir(snapshots.roundDir)
		b.finishRoundFailure(job, brokerError(CodeUserError, "snapshot artifacts", err, nil))
		return
	}
	b.touchRoundStatus(job)

	promptText, schemaBytes := prompt.Build(prompt.Request{
		Artifacts:     snapshots.promptArtifacts,
		ReviewContext: job.reviewContext,
		ReviewFocus:   job.reviewFocus,
		MaxFindings:   job.maxFindings,
	})

	promptLogPath := filepath.Join(snapshots.roundDir, roundPromptName)
	if err := os.WriteFile(promptLogPath, []byte(promptText), 0o600); err != nil {
		cleanupRoundDir(snapshots.roundDir)
		b.finishRoundFailure(job, brokerError(CodeInternalError, "write prompt log", err, map[string]any{"prompt_path": promptLogPath}))
		return
	}

	b.touchRoundStatus(job)
	resp, err := job.reviewer.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:    promptText,
		Artifacts: snapshots.reviewerArtifacts,
		Schema:    schemaBytes,
		SessionMeta: reviewer.SessionMetadata{
			SessionID:   job.sessionID,
			RoundNumber: job.roundNumber,
		},
	})
	if err != nil {
		cleanupRoundDir(snapshots.roundDir)
		b.finishRoundFailure(job, brokerError(CodeReviewerFailed, "reviewer failed", err, map[string]any{"reviewer": job.reviewerName}))
		return
	}

	output, err := schema.ParseReviewOutput(resp.Raw)
	if err != nil {
		cleanupRoundDir(snapshots.roundDir)
		err := brokerError(CodeReviewerFailed, "reviewer output failed schema validation", err, map[string]any{
			"reviewer": job.reviewerName,
			"raw":      string(resp.Raw),
		})
		b.finishRoundFailure(job, err)
		return
	}
	if err := schema.ValidateFindingLimit(output, job.maxFindings); err != nil {
		cleanupRoundDir(snapshots.roundDir)
		err := brokerError(CodeReviewerFailed, "reviewer output exceeded max findings", err, map[string]any{
			"reviewer":     job.reviewerName,
			"max_findings": job.maxFindings,
			"findings":     schema.FindingCount(output),
			"concerns":     len(output.Concerns),
			"questions":    len(output.Questions),
			"raw":          string(resp.Raw),
		})
		b.finishRoundFailure(job, err)
		return
	}

	logPath := filepath.Join(snapshots.roundDir, roundLogName)
	results := []ReviewerResult{{
		ReviewerName: job.reviewerName,
		Raw:          append(json.RawMessage(nil), resp.Raw...),
		UsageNotes:   resp.UsageNotes,
	}}
	if err := roundlog.WriteInitial(logPath, roundlog.Entry{
		SessionID:   job.sessionID,
		RoundNumber: job.roundNumber,
		OpenedAt:    job.startedAt,
		Verdict:     output.Verdict,
		PromptPath:  roundPromptName,
		Manifest:    toRoundlogManifest(snapshots.manifest),
		Reviewers:   toRoundlogReviewers(results),
	}); err != nil {
		cleanupRoundDir(snapshots.roundDir)
		b.finishRoundFailure(job, brokerError(CodeInternalError, "write round log", err, map[string]any{"log_path": logPath}))
		return
	}

	b.finishRoundSuccess(job, CollectedRoundResponse{
		RoundNumber: job.roundNumber,
		LogPath:     logPath,
		Manifest:    cloneManifest(snapshots.manifest),
		Reviewers:   cloneResults(results),
	}, refsFromOutput(output))
}

// CollectRound returns a completed round response.
func (b *Broker) CollectRound(ctx context.Context, req CollectRoundRequest) (CollectedRoundResponse, error) {
	if err := ctx.Err(); err != nil {
		return CollectedRoundResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(req.SessionID)
	if err != nil {
		return CollectedRoundResponse{}, err
	}
	roundNumber := req.RoundNumber
	if roundNumber == 0 {
		if s.activeJob != nil {
			roundNumber = s.activeJob.roundNumber
		} else if s.lastRoundJob != nil {
			roundNumber = s.lastRoundJob.roundNumber
		} else if len(s.rounds) > 0 {
			roundNumber = s.rounds[len(s.rounds)-1].number
		}
	}
	if s.activeJob != nil && s.activeJob.roundNumber == roundNumber {
		return CollectedRoundResponse{}, brokerError(CodeConflict, "round is still running", nil, map[string]any{
			"session_id":   req.SessionID,
			"round_number": roundNumber,
			"status_path":  s.activeJob.statusPath,
		})
	}
	if s.lastRoundJob != nil && s.lastRoundJob.roundNumber == roundNumber {
		if s.lastRoundJob.state == roundStateFailed {
			return CollectedRoundResponse{}, s.lastRoundJob.err
		}
		if s.lastRoundJob.state == roundStateCompleted {
			return cloneCollectedRoundResponse(s.lastRoundJob.result), nil
		}
	}
	if round := s.findRound(roundNumber); round != nil {
		return CollectedRoundResponse{
			RoundNumber: round.number,
			LogPath:     round.logPath,
			Manifest:    cloneManifest(round.manifest),
			Reviewers:   cloneResults(round.results),
		}, nil
	}
	return CollectedRoundResponse{}, brokerError(CodeNotFound, "round not found", nil, map[string]any{"round_number": req.RoundNumber})
}

func (b *Broker) finishRoundSuccess(job *roundJob, response CollectedRoundResponse, refs map[string]refKind) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(job.sessionID)
	if err != nil {
		job.finish(roundStateFailed, "", err)
		close(job.done)
		return
	}
	round := &round{
		number:    job.roundNumber,
		openedAt:  job.startedAt,
		logPath:   response.LogPath,
		notesPath: filepath.Join(filepath.Dir(response.LogPath), roundNotesName),
		manifest:  cloneManifest(response.Manifest),
		results:   cloneResults(response.Reviewers),
		refs:      refs,
	}
	s.rounds = append(s.rounds, round)
	s.lastError = nil
	job.result = cloneCollectedRoundResponse(response)
	job.finish(roundStateCompleted, response.LogPath, nil)
	s.activeJob = nil
	s.lastRoundJob = job
	_ = b.persistSessionLocked(s)
	close(job.done)
}

func (b *Broker) finishRoundFailure(job *roundJob, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, sessionErr := b.session(job.sessionID)
	if sessionErr != nil {
		job.finish(roundStateFailed, "", sessionErr)
		close(job.done)
		return
	}
	job.finish(roundStateFailed, "", err)
	s.recordError(err)
	s.activeJob = nil
	s.lastRoundJob = job
	_ = b.persistSessionLocked(s)
	close(job.done)
}

func (b *Broker) touchRoundStatus(job *roundJob) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(job.sessionID)
	if err != nil {
		return
	}
	job.updatedAt = time.Now().UTC()
	_ = b.persistSessionLocked(s)
}

// RecordRoundNotes records a round's commentary and decisions in a sibling
// `_notes.md` file. The immutable round log is not touched.
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
		err := brokerError(CodeNotFound, "round not found", nil, map[string]any{"round_number": req.RoundNumber})
		s.recordError(err)
		return RecordRoundNotesResponse{}, err
	}
	if strings.TrimSpace(req.Commentary) == "" && len(req.Decisions) == 0 {
		err := brokerError(CodeUserError, "commentary and decisions are empty", nil, nil)
		s.recordError(err)
		return RecordRoundNotesResponse{}, err
	}

	decisions := cloneDecisions(req.Decisions)
	for _, decision := range decisions {
		if _, ok := round.refs[decision.Ref]; !ok {
			err := brokerError(CodeUserError, "decision ref is unknown", nil, map[string]any{"ref": decision.Ref})
			s.recordError(err)
			return RecordRoundNotesResponse{}, err
		}
		if !validDisposition(decision.Disposition) {
			err := brokerError(CodeUserError, "decision disposition is invalid", nil, map[string]any{"disposition": decision.Disposition})
			s.recordError(err)
			return RecordRoundNotesResponse{}, err
		}
	}

	if err := roundlog.WriteNotes(round.notesPath, req.Commentary, toRoundlogDecisions(decisions)); err != nil {
		err := brokerError(CodeInternalError, "write round notes", err, map[string]any{"notes_path": round.notesPath})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return RecordRoundNotesResponse{}, err
	}
	round.hasNotes = true
	round.decisions = decisions
	round.commentary = req.Commentary
	s.lastError = nil
	if err := b.persistSessionLocked(s); err != nil {
		err := brokerError(CodeInternalError, "write session status", err, nil)
		s.recordError(err)
		return RecordRoundNotesResponse{}, err
	}

	return RecordRoundNotesResponse{
		RoundNumber:        req.RoundNumber,
		LogPath:            round.notesPath,
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
		err := brokerError(CodeConflict, "session is already closed", nil, map[string]any{"session_id": req.SessionID})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return CloseSessionResponse{}, err
	}
	if s.activeJob != nil {
		err := brokerError(CodeConflict, "a review round is still running", nil, map[string]any{
			"session_id":   req.SessionID,
			"round_number": s.activeJob.roundNumber,
		})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return CloseSessionResponse{}, err
	}

	closedAt := time.Now().UTC()
	s.state = stateClosed
	s.closedAt = &closedAt
	s.lastError = nil
	if err := b.persistSessionLocked(s); err != nil {
		err := brokerError(CodeInternalError, "write session status", err, nil)
		s.recordError(err)
		return CloseSessionResponse{}, err
	}

	return CloseSessionResponse{
		SessionID: s.id,
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
	return s.status(b.options), nil
}

func (b *Broker) session(id string) (*session, error) {
	s, ok := b.sessions[id]
	if !ok {
		return nil, brokerError(CodeNotFound, "session not found", nil, map[string]any{"session_id": id})
	}
	return s, nil
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

func (s *session) status(options Options) SessionStatusResponse {
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
		SessionID:            s.id,
		State:                s.state,
		OpenedAt:             s.openedAt,
		ClosedAt:             cloneTimePtr(s.closedAt),
		MaxFindings:          options.MaxFindings,
		ReviewContextPresent: strings.TrimSpace(options.ReviewContext) != "",
		ReviewFocusPresent:   strings.TrimSpace(options.ReviewFocus) != "",
		RoundCount:           len(s.rounds),
		Reviewer:             options.ReviewerInfo,
		LastError:            cloneErrorInfo(s.lastError),
		ActiveRound:          jobStatusPtr(s.activeJob, options.ReviewerInfo.Name),
		Rounds:               rounds,
	}
}

func (b *Broker) persistSessionLocked(s *session) error {
	return monitor.WriteStatus(monitor.StatusPath(s.dir), monitorSessionStatus(s.status(b.options)))
}

func (b *Broker) monitorCommand(sessionID string) string {
	if b.options.ConfigPath == "" {
		return fmt.Sprintf("mercurius monitor --session %s --wait", sessionID)
	}
	return fmt.Sprintf("mercurius monitor --config %s --session %s --wait", b.options.ConfigPath, sessionID)
}

func (s *session) recordError(err error) {
	info := ErrorInfoFrom(err)
	if info == nil {
		s.lastError = nil
		return
	}
	info.At = time.Now().UTC()
	s.lastError = info
}

func (j *roundJob) finish(state string, logPath string, err error) {
	now := time.Now().UTC()
	j.state = state
	j.updatedAt = now
	j.completedAt = &now
	j.logPath = logPath
	j.err = err
	j.errorInfo = ErrorInfoFrom(err)
	if j.errorInfo != nil {
		j.errorInfo.At = now
	}
}

func (j *roundJob) status(reviewerName string) RoundStatusResponse {
	return RoundStatusResponse{
		SessionID:   j.sessionID,
		RoundNumber: j.roundNumber,
		State:       j.state,
		Reviewer:    reviewerName,
		StartedAt:   j.startedAt,
		UpdatedAt:   j.updatedAt,
		CompletedAt: cloneTimePtr(j.completedAt),
		LogPath:     j.logPath,
		StatusPath:  j.statusPath,
		Error:       cloneErrorInfo(j.errorInfo),
	}
}

func jobStatusPtr(job *roundJob, reviewerName string) *RoundStatusResponse {
	if job == nil {
		return nil
	}
	status := job.status(reviewerName)
	return &status
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

		if !filepath.IsAbs(artifact.Path) {
			return nil, fmt.Errorf("artifact '%s' path is not absolute", artifact.Name)
		}
		if _, err := os.ReadFile(artifact.Path); err != nil {
			return nil, fmt.Errorf("artifact '%s' is not readable: %w", artifact.Name, err)
		}
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
	if strings.HasPrefix(name, "_") {
		return fmt.Errorf("artifact name '%s' cannot begin with '_' (reserved for broker meta files in the round directory)", name)
	}
	if !safeArtifactName.MatchString(name) {
		return fmt.Errorf("artifact name '%s' is unsafe", name)
	}
	return nil
}

func snapshotArtifacts(sessionDir string, roundNumber int, artifacts []Artifact) (snapshotResult, error) {
	roundDir := filepath.Join(sessionDir, fmt.Sprintf("round-%02d", roundNumber))
	result := snapshotResult{roundDir: roundDir}
	if err := os.MkdirAll(roundDir, 0o700); err != nil {
		return result, err
	}

	for _, artifact := range artifacts {
		content, err := os.ReadFile(artifact.Path)
		if err != nil {
			return result, err
		}
		snapshotPath := filepath.Join(roundDir, artifact.Name)
		if err := os.WriteFile(snapshotPath, content, 0o600); err != nil {
			return result, err
		}
		hash := sha256.Sum256(content)
		hashText := "sha256:" + hex.EncodeToString(hash[:])
		result.manifest = append(result.manifest, ArtifactManifestEntry{
			Name:         artifact.Name,
			SourcePath:   artifact.Path,
			SnapshotPath: snapshotPath,
			Size:         int64(len(content)),
			Hash:         hashText,
		})
		result.promptArtifacts = append(result.promptArtifacts, prompt.Artifact{
			Name:         artifact.Name,
			SourcePath:   artifact.Path,
			SnapshotPath: snapshotPath,
			Hash:         hashText,
			Content:      append([]byte(nil), content...),
		})
		result.reviewerArtifacts = append(result.reviewerArtifacts, reviewer.Artifact{
			Name: artifact.Name,
			Path: snapshotPath,
		})
	}
	return result, nil
}

func cleanupRoundDir(roundDir string) {
	if roundDir != "" {
		_ = os.RemoveAll(roundDir)
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
	return disposition == "fixed" || disposition == "rejected" || disposition == "deferred"
}

func refsFromOutput(output schema.ReviewOutput) map[string]refKind {
	refs := map[string]refKind{}
	for _, concern := range output.Concerns {
		refs[concern.ID] = refKindConcern
	}
	for _, question := range output.Questions {
		refs[question.ID] = refKindQuestion
	}
	for _, note := range output.AdvisoryNotes {
		refs[note.ID] = refKindAdvisory
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

func cloneErrorInfo(info *ErrorInfo) *ErrorInfo {
	if info == nil {
		return nil
	}
	out := *info
	out.Details = cloneDetails(info.Details)
	return &out
}

func monitorSessionStatus(status SessionStatusResponse) monitor.SessionStatus {
	return monitor.SessionStatus{
		SessionID:            status.SessionID,
		State:                status.State,
		OpenedAt:             status.OpenedAt,
		ClosedAt:             cloneTimePtr(status.ClosedAt),
		MaxFindings:          status.MaxFindings,
		ReviewContextPresent: status.ReviewContextPresent,
		ReviewFocusPresent:   status.ReviewFocusPresent,
		RoundCount:           status.RoundCount,
		Reviewer:             monitorReviewer(status.Reviewer),
		LastError:            monitorErrorInfo(status.LastError),
		ActiveRound:          monitorRoundJob(status.ActiveRound),
		Rounds:               monitorRounds(status.Rounds),
	}
}

func monitorReviewer(r ReviewerInfo) monitor.ReviewerInfo {
	return monitor.ReviewerInfo{Name: r.Name, Impl: r.Impl, Model: r.Model}
}

func monitorRounds(rounds []RoundStatus) []monitor.RoundStatus {
	out := make([]monitor.RoundStatus, 0, len(rounds))
	for _, round := range rounds {
		out = append(out, monitor.RoundStatus{
			RoundNumber:   round.RoundNumber,
			OpenedAt:      round.OpenedAt,
			LogPath:       round.LogPath,
			HasNotes:      round.HasNotes,
			DecisionCount: round.DecisionCount,
		})
	}
	return out
}

func monitorRoundJob(status *RoundStatusResponse) *monitor.RoundJob {
	if status == nil {
		return nil
	}
	return &monitor.RoundJob{
		SessionID:   status.SessionID,
		RoundNumber: status.RoundNumber,
		State:       status.State,
		Reviewer:    status.Reviewer,
		StartedAt:   status.StartedAt,
		UpdatedAt:   status.UpdatedAt,
		CompletedAt: cloneTimePtr(status.CompletedAt),
		LogPath:     status.LogPath,
		StatusPath:  status.StatusPath,
		Error:       monitorErrorInfo(status.Error),
	}
}

func monitorErrorInfo(info *ErrorInfo) *monitor.ErrorInfo {
	if info == nil {
		return nil
	}
	return &monitor.ErrorInfo{
		Code:    info.Code,
		Message: info.Message,
		Details: cloneDetails(info.Details),
		At:     info.At,
	}
}

func cloneCollectedRoundResponse(response CollectedRoundResponse) CollectedRoundResponse {
	return CollectedRoundResponse{
		RoundNumber: response.RoundNumber,
		LogPath:     response.LogPath,
		Manifest:    cloneManifest(response.Manifest),
		Reviewers:   cloneResults(response.Reviewers),
	}
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

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

	"github.com/michaelquigley/mercurius/internal/monitor"
	"github.com/michaelquigley/mercurius/internal/prompt"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/roundlog"
	"github.com/michaelquigley/mercurius/internal/schema"
)

const (
	defaultBudget        = 4
	defaultMaxFindings   = 6
	stateActive          = "active"
	stateClosed          = "closed"
	roundStateRunning    = "running"
	roundStateCompleted  = "completed"
	roundStateFailed     = "failed"
	reviewContextSession = "session"
	reviewContextConfig  = "config"
	reviewContextNone    = "none"
	convergenceNone      = "none"
	convergenceWatch     = "watch"
	convergenceClose     = "consider_closing"
)

var safeArtifactName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Broker orchestrates in-memory sessions and review rounds.
type Broker struct {
	mu           sync.Mutex
	options      Options
	reviewers    map[string]ReviewerSpec
	reviewerList []ReviewerSpec
	sessions     map[string]*session
}

type session struct {
	id                  string
	dir                 string
	state               string
	verdict             *string
	openedAt            time.Time
	closedAt            *time.Time
	budget              int
	maxFindings         int
	artifacts           []Artifact
	reviewerName        string
	reviewerInfo        ReviewerInfo
	reviewer            reviewer.Reviewer
	rounds              []*round
	reviewContext       string
	reviewContextSource string
	reviewFocus         string
	lastError           *ErrorInfo
	activeJob           *roundJob
	lastRoundJob        *roundJob
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

type roundJob struct {
	sessionID      string
	sessionDir     string
	roundNumber    int
	state          string
	reviewerName   string
	reviewer       reviewer.Reviewer
	artifacts      []Artifact
	hasOverride    bool
	priorDecisions []reviewer.PriorDecision
	reviewContext  string
	decisionsLog   string
	reviewFocus    string
	maxFindings    int
	startedAt      time.Time
	updatedAt      time.Time
	completedAt    *time.Time
	logPath        string
	statusPath     string
	eventsPath     string
	result         CollectedRoundResponse
	err            error
	errorInfo      *ErrorInfo
	done           chan struct{}
}

// New creates an in-memory broker.
func New(options Options) *Broker {
	if options.DefaultBudget == 0 {
		options.DefaultBudget = defaultBudget
	}
	if options.MaxFindings <= 0 {
		options.MaxFindings = defaultMaxFindings
	}
	reviewers := make(map[string]ReviewerSpec, len(options.Reviewers))
	for _, spec := range options.Reviewers {
		reviewers[spec.Name] = spec
	}
	return &Broker{
		options:      options,
		reviewers:    reviewers,
		reviewerList: append([]ReviewerSpec(nil), options.Reviewers...),
		sessions:     map[string]*session{},
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

	reviewContext, reviewContextSource := effectiveReviewContext(req.ReviewContext, b.options.ReviewContext)
	openedAt := time.Now().UTC()
	s := &session{
		id:                  id,
		dir:                 dir,
		state:               stateActive,
		openedAt:            openedAt,
		budget:              budget,
		maxFindings:         b.options.MaxFindings,
		artifacts:           artifacts,
		reviewerName:        spec.Name,
		reviewerInfo:        reviewerInfo(spec),
		reviewer:            reviewerImpl,
		reviewContext:       reviewContext,
		reviewContextSource: reviewContextSource,
		reviewFocus:         b.options.ReviewFocus,
	}
	b.sessions[id] = s

	if err := b.appendEventLocked(s, monitor.Event{
		At:        openedAt,
		Event:     "session_opened",
		SessionID: id,
		State:     stateActive,
	}); err != nil {
		delete(b.sessions, id)
		_ = os.RemoveAll(dir)
		return OpenSessionResponse{}, brokerError(CodeInternalError, "write session event", err, nil)
	}
	if err := b.persistSessionLocked(s); err != nil {
		delete(b.sessions, id)
		_ = os.RemoveAll(dir)
		return OpenSessionResponse{}, brokerError(CodeInternalError, "write session status", err, nil)
	}

	return OpenSessionResponse{
		SessionID:            id,
		SessionDir:           dir,
		OpenedAt:             openedAt,
		Budget:               budget,
		BudgetRemaining:      budget,
		MaxFindings:          b.options.MaxFindings,
		ReviewContextSource:  reviewContextSource,
		ReviewContextPresent: reviewContext != "",
		RoundsUsed:           0,
		Reviewers:            []ReviewerInfo{reviewerInfo(spec)},
		Artifacts:            registeredArtifacts(artifacts),
	}, nil
}

// StartReviewRound starts a background review round.
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
		err := brokerError(CodeSessionClosed, "session is closed", nil, map[string]any{"session_id": req.SessionID})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		b.mu.Unlock()
		return StartReviewRoundResponse{}, err
	}
	if s.activeJob != nil {
		err := brokerError(CodeRoundInProgress, "a review round is already running", nil, map[string]any{
			"session_id":   req.SessionID,
			"round_number": s.activeJob.roundNumber,
		})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		b.mu.Unlock()
		return StartReviewRoundResponse{}, err
	}
	if len(s.rounds) >= s.budget {
		err := brokerError(CodeBudgetExhausted, "session budget is exhausted", nil, map[string]any{"session_id": req.SessionID, "budget": s.budget})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		b.mu.Unlock()
		return StartReviewRoundResponse{}, err
	}

	artifacts := cloneArtifacts(s.artifacts)
	hasOverride := req.Artifacts != nil
	if hasOverride {
		validated, err := validateArtifacts(req.Artifacts)
		if err != nil {
			err := brokerError(CodeInvalidArtifacts, "invalid artifact override", err, nil)
			s.recordError(err)
			_ = b.persistSessionLocked(s)
			b.mu.Unlock()
			return StartReviewRoundResponse{}, err
		}
		artifacts = validated
	}

	roundNumber := len(s.rounds) + 1
	startedAt := time.Now().UTC()
	job := &roundJob{
		sessionID:      s.id,
		sessionDir:     s.dir,
		roundNumber:    roundNumber,
		state:          roundStateRunning,
		reviewerName:   s.reviewerName,
		reviewer:       s.reviewer,
		artifacts:      artifacts,
		hasOverride:    hasOverride,
		priorDecisions: s.priorDecisions(),
		reviewContext:  s.reviewContext,
		decisionsLog:   s.decisionsLogText(),
		reviewFocus:    s.reviewFocus,
		maxFindings:    s.maxFindings,
		startedAt:      startedAt,
		updatedAt:      startedAt,
		statusPath:     monitor.StatusPath(s.dir),
		eventsPath:     monitor.EventsPath(s.dir),
		done:           make(chan struct{}),
	}
	s.activeJob = job
	s.lastRoundJob = job
	s.lastError = nil
	if err := b.appendEventLocked(s, monitor.Event{
		At:          startedAt,
		Event:       "round_started",
		SessionID:   s.id,
		RoundNumber: roundNumber,
		Reviewer:    s.reviewerName,
		State:       roundStateRunning,
	}); err != nil {
		s.activeJob = nil
		s.lastRoundJob = nil
		s.recordError(brokerError(CodeInternalError, "write round event", err, nil))
		_ = b.persistSessionLocked(s)
		b.mu.Unlock()
		return StartReviewRoundResponse{}, brokerError(CodeInternalError, "write round event", err, nil)
	}
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
		Reviewer:       s.reviewerName,
		StartedAt:      startedAt,
		StatusPath:     job.statusPath,
		EventsPath:     job.eventsPath,
		MonitorCommand: b.monitorCommand(s.id),
		NextAction:     "tell the user this review is running; they can monitor it with the monitor_command and re-engage you when the round completes",
	}
	b.mu.Unlock()

	go b.executeRoundJob(job)
	return response, nil
}

func (b *Broker) executeRoundJob(job *roundJob) {
	b.markRoundEvent(job, "artifacts_snapshotting", "")

	snapshots, err := snapshotArtifacts(job.sessionDir, job.roundNumber, job.artifacts)
	if err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		b.finishRoundFailure(job, brokerError(CodeInvalidArtifacts, "snapshot artifacts", err, nil))
		return
	}
	b.markRoundEvent(job, "artifacts_snapshotted", "")

	promptText, schemaBytes := prompt.Build(prompt.Request{
		Artifacts:      snapshots.promptArtifacts,
		PriorDecisions: job.priorDecisions,
		ReviewContext:  job.reviewContext,
		DecisionsLog:   job.decisionsLog,
		ReviewFocus:    job.reviewFocus,
		MaxFindings:    job.maxFindings,
	})

	promptLogPath := filepath.Join(snapshots.snapshotDir, "_prompt.md")
	if err := os.WriteFile(promptLogPath, []byte(promptText), 0o600); err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		b.finishRoundFailure(job, brokerError(CodeInternalError, "write prompt log", err, map[string]any{"prompt_path": promptLogPath}))
		return
	}

	b.markRoundEvent(job, "reviewer_started", "")
	resp, err := job.reviewer.Review(context.Background(), reviewer.ReviewRequest{
		Prompt:    promptText,
		Artifacts: snapshots.reviewerArtifacts,
		Schema:    schemaBytes,
		SessionMeta: reviewer.SessionMetadata{
			SessionID:      job.sessionID,
			RoundNumber:    job.roundNumber,
			PriorDecisions: job.priorDecisions,
		},
	})
	if err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		b.finishRoundFailure(job, brokerError(CodeReviewerFailed, "reviewer failed", err, map[string]any{"reviewer": job.reviewerName}))
		return
	}
	b.markRoundEvent(job, "reviewer_completed", "")

	output, err := schema.ParseReviewOutput(resp.Raw)
	if err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		err := brokerError(CodeSchemaViolation, "reviewer output failed schema validation", err, map[string]any{
			"reviewer": job.reviewerName,
			"raw":      string(resp.Raw),
		})
		b.finishRoundFailure(job, err)
		return
	}
	if err := schema.ValidateFindingLimit(output, job.maxFindings); err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
		err := brokerError(CodeSchemaViolation, "reviewer output exceeded max findings", err, map[string]any{
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

	logPath := filepath.Join(job.sessionDir, fmt.Sprintf("round-%02d.md", job.roundNumber))
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
		PromptPath:  filepath.ToSlash(filepath.Join("snapshots", fmt.Sprintf("round-%02d", job.roundNumber), "_prompt.md")),
		Manifest:    toRoundlogManifest(snapshots.manifest),
		Reviewers:   toRoundlogReviewers(results),
	}); err != nil {
		cleanupSnapshot(snapshots.snapshotDir)
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

// RoundStatus returns a background round status.
func (b *Broker) RoundStatus(ctx context.Context, req RoundStatusRequest) (RoundStatusResponse, error) {
	if err := ctx.Err(); err != nil {
		return RoundStatusResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(req.SessionID)
	if err != nil {
		return RoundStatusResponse{}, err
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
		return s.activeJob.status(), nil
	}
	if s.lastRoundJob != nil && s.lastRoundJob.roundNumber == roundNumber {
		return s.lastRoundJob.status(), nil
	}
	if round := s.findRound(roundNumber); round != nil {
		completedAt := round.openedAt
		return RoundStatusResponse{
			SessionID:   s.id,
			RoundNumber: round.number,
			State:       roundStateCompleted,
			Reviewer:    s.reviewerName,
			StartedAt:   round.openedAt,
			UpdatedAt:   round.openedAt,
			CompletedAt: &completedAt,
			LogPath:     round.logPath,
			StatusPath:  monitor.StatusPath(s.dir),
			EventsPath:  monitor.EventsPath(s.dir),
		}, nil
	}
	return RoundStatusResponse{}, brokerError(CodeUnknownRound, "round not found", nil, map[string]any{"round_number": req.RoundNumber})
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
		return CollectedRoundResponse{}, brokerError(CodeRoundInProgress, "round is still running", nil, map[string]any{
			"session_id":   req.SessionID,
			"round_number": roundNumber,
			"status_path":  s.activeJob.statusPath,
			"events_path":  s.activeJob.eventsPath,
		})
	}
	if s.lastRoundJob != nil && s.lastRoundJob.roundNumber == roundNumber {
		if s.lastRoundJob.state == roundStateFailed {
			return CollectedRoundResponse{}, s.lastRoundJob.err
		}
		if s.lastRoundJob.state == roundStateCompleted {
			response := cloneCollectedRoundResponse(s.lastRoundJob.result)
			response.Convergence = s.convergence()
			return response, nil
		}
	}
	if round := s.findRound(roundNumber); round != nil {
		return CollectedRoundResponse{
			RoundNumber: round.number,
			LogPath:     round.logPath,
			Manifest:    cloneManifest(round.manifest),
			Reviewers:   cloneResults(round.results),
			Convergence: s.convergence(),
		}, nil
	}
	return CollectedRoundResponse{}, brokerError(CodeUnknownRound, "round not found", nil, map[string]any{"round_number": req.RoundNumber})
}

func (b *Broker) finishRoundSuccess(job *roundJob, response CollectedRoundResponse, refs map[string]struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(job.sessionID)
	if err != nil {
		job.finish(roundStateFailed, "", err)
		close(job.done)
		return
	}
	round := &round{
		number:   job.roundNumber,
		openedAt: job.startedAt,
		logPath:  response.LogPath,
		manifest: cloneManifest(response.Manifest),
		results:  cloneResults(response.Reviewers),
		refs:     refs,
	}
	if job.hasOverride {
		s.artifacts = cloneArtifacts(job.artifacts)
	}
	s.rounds = append(s.rounds, round)
	s.lastError = nil
	response.Convergence = s.convergence()
	job.result = cloneCollectedRoundResponse(response)
	job.finish(roundStateCompleted, response.LogPath, nil)
	s.activeJob = nil
	s.lastRoundJob = job
	_ = b.appendEventLocked(s, monitor.Event{
		At:          job.updatedAt,
		Event:       "round_completed",
		SessionID:   job.sessionID,
		RoundNumber: job.roundNumber,
		Reviewer:    job.reviewerName,
		State:       roundStateCompleted,
		LogPath:     response.LogPath,
	})
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
	_ = b.appendEventLocked(s, monitor.Event{
		At:          job.updatedAt,
		Event:       "round_failed",
		SessionID:   job.sessionID,
		RoundNumber: job.roundNumber,
		Reviewer:    job.reviewerName,
		State:       roundStateFailed,
		Error:       monitorErrorInfo(job.errorInfo),
	})
	_ = b.persistSessionLocked(s)
	close(job.done)
}

func (b *Broker) markRoundEvent(job *roundJob, event string, logPath string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, err := b.session(job.sessionID)
	if err != nil {
		return
	}
	job.updatedAt = time.Now().UTC()
	if logPath != "" {
		job.logPath = logPath
	}
	_ = b.appendEventLocked(s, monitor.Event{
		At:          job.updatedAt,
		Event:       event,
		SessionID:   job.sessionID,
		RoundNumber: job.roundNumber,
		Reviewer:    job.reviewerName,
		State:       job.state,
		LogPath:     logPath,
	})
	_ = b.persistSessionLocked(s)
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
		err := brokerError(CodeUnknownRound, "round not found", nil, map[string]any{"round_number": req.RoundNumber})
		s.recordError(err)
		return RecordRoundNotesResponse{}, err
	}
	if strings.TrimSpace(req.Commentary) == "" && len(req.Decisions) == 0 {
		err := brokerError(CodeEmptyNotes, "commentary and decisions are empty", nil, nil)
		s.recordError(err)
		return RecordRoundNotesResponse{}, err
	}

	decisions := cloneDecisions(req.Decisions)
	for _, decision := range decisions {
		if _, ok := round.refs[decision.Ref]; !ok {
			err := brokerError(CodeUnknownRef, "decision ref is unknown", nil, map[string]any{"ref": decision.Ref})
			s.recordError(err)
			return RecordRoundNotesResponse{}, err
		}
		if !validDisposition(decision.Disposition) {
			err := brokerError(CodeInvalidDecision, "decision disposition is invalid", nil, map[string]any{"disposition": decision.Disposition})
			s.recordError(err)
			return RecordRoundNotesResponse{}, err
		}
	}

	if err := roundlog.UpdateNotes(round.logPath, req.Commentary, toRoundlogDecisions(decisions)); err != nil {
		err := brokerError(CodeInternalError, "update round notes", err, map[string]any{"log_path": round.logPath})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return RecordRoundNotesResponse{}, err
	}
	round.hasNotes = true
	round.decisions = decisions
	if err := b.writeDecisionsLogLocked(s); err != nil {
		err := brokerError(CodeInternalError, "write decisions log", err, map[string]any{"path": decisionsLogPath(s.dir)})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return RecordRoundNotesResponse{}, err
	}
	s.lastError = nil
	now := time.Now().UTC()
	if err := b.appendEventLocked(s, monitor.Event{
		At:          now,
		Event:       "notes_recorded",
		SessionID:   s.id,
		RoundNumber: req.RoundNumber,
		State:       s.state,
		LogPath:     round.logPath,
	}); err != nil {
		err := brokerError(CodeInternalError, "write notes event", err, nil)
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return RecordRoundNotesResponse{}, err
	}
	if err := b.persistSessionLocked(s); err != nil {
		err := brokerError(CodeInternalError, "write session status", err, nil)
		s.recordError(err)
		return RecordRoundNotesResponse{}, err
	}

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
		err := brokerError(CodeAlreadyClosed, "session is already closed", nil, map[string]any{"session_id": req.SessionID})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return CloseSessionResponse{}, err
	}
	if s.activeJob != nil {
		err := brokerError(CodeRoundInProgress, "a review round is still running", nil, map[string]any{
			"session_id":   req.SessionID,
			"round_number": s.activeJob.roundNumber,
		})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return CloseSessionResponse{}, err
	}
	if !validCloseVerdict(req.Verdict) {
		err := brokerError(CodeInvalidVerdict, "session verdict is invalid", nil, map[string]any{"verdict": req.Verdict})
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return CloseSessionResponse{}, err
	}

	closedAt := time.Now().UTC()
	s.state = stateClosed
	s.verdict = &req.Verdict
	s.closedAt = &closedAt
	s.lastError = nil
	if err := b.appendEventLocked(s, monitor.Event{
		At:        closedAt,
		Event:     "session_closed",
		SessionID: s.id,
		State:     stateClosed,
	}); err != nil {
		err := brokerError(CodeInternalError, "write session event", err, nil)
		s.recordError(err)
		_ = b.persistSessionLocked(s)
		return CloseSessionResponse{}, err
	}
	if err := b.persistSessionLocked(s); err != nil {
		err := brokerError(CodeInternalError, "write session status", err, nil)
		s.recordError(err)
		return CloseSessionResponse{}, err
	}

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

// ListReviewers returns configured reviewer metadata.
func (b *Broker) ListReviewers(ctx context.Context) (ListReviewersResponse, error) {
	if err := ctx.Err(); err != nil {
		return ListReviewersResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	reviewers := make([]ReviewerInfo, 0, len(b.reviewerList))
	for _, spec := range b.reviewerList {
		reviewers = append(reviewers, reviewerInfo(spec))
	}
	return ListReviewersResponse{Reviewers: reviewers}, nil
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
			return ReviewerSpec{}, brokerError(CodePanelModeUnsupported, "exactly one reviewer must be selected", nil, nil)
		}
		for _, spec := range b.reviewers {
			return spec, nil
		}
	}
	if len(names) != 1 {
		return ReviewerSpec{}, brokerError(CodePanelModeUnsupported, "v1 supports exactly one reviewer", nil, map[string]any{"reviewers": names})
	}
	spec, ok := b.reviewers[names[0]]
	if !ok {
		return ReviewerSpec{}, brokerError(CodeUnknownReviewer, "reviewer is not configured", nil, map[string]any{"reviewer": names[0]})
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

func (s *session) decisionsLogText() string {
	var b strings.Builder
	b.WriteString("# session decisions log\n\n")
	hasDecisions := false
	for _, round := range s.rounds {
		if len(round.decisions) == 0 {
			continue
		}
		hasDecisions = true
		b.WriteString(fmt.Sprintf("## round %d\n", round.number))
		for _, decision := range round.decisions {
			note := strings.TrimSpace(decision.Note)
			if note == "" {
				note = "no note recorded"
			}
			b.WriteString(fmt.Sprintf("- %s (%s): %s\n", decision.Ref, decision.Disposition, note))
		}
		b.WriteString("\n")
	}
	if !hasDecisions {
		b.WriteString("_no decisions recorded yet_\n")
	}
	return b.String()
}

func (s *session) convergence() Convergence {
	convergence := Convergence{
		Signal:                      convergenceNone,
		Message:                     "No convergence signal yet.",
		LatestBlockingFindings:      -1,
		PreviousBlockingFindings:    -1,
		DeclinedOrDeferredDecisions: 0,
		AcceptedDecisions:           0,
	}
	if len(s.rounds) == 0 {
		convergence.LatestBlockingFindings = 0
		convergence.PreviousBlockingFindings = 0
		return convergence
	}

	latestOutput, latestOK := firstParsedOutput(s.rounds[len(s.rounds)-1])
	if latestOK {
		convergence.LatestBlockingFindings = schema.FindingCount(latestOutput)
	} else {
		convergence.LatestBlockingFindings = 0
	}
	if len(s.rounds) >= 2 {
		previousOutput, previousOK := firstParsedOutput(s.rounds[len(s.rounds)-2])
		if previousOK {
			convergence.PreviousBlockingFindings = schema.FindingCount(previousOutput)
		} else {
			convergence.PreviousBlockingFindings = 0
		}
	} else {
		convergence.PreviousBlockingFindings = 0
	}
	for _, round := range s.rounds {
		for _, decision := range round.decisions {
			switch decision.Disposition {
			case "accepted":
				convergence.AcceptedDecisions++
			case "rejected", "deferred":
				convergence.DeclinedOrDeferredDecisions++
			}
		}
	}

	if latestOK && convergence.LatestBlockingFindings == 0 {
		convergence.Signal = convergenceClose
		convergence.Message = "Latest review says the artifacts are ready or has no blocking findings; consider closing the session."
		return convergence
	}
	decisionCount := convergence.AcceptedDecisions + convergence.DeclinedOrDeferredDecisions
	if len(s.rounds) >= 2 && (convergence.LatestBlockingFindings <= 2 || (decisionCount > 0 && convergence.DeclinedOrDeferredDecisions >= convergence.AcceptedDecisions)) {
		convergence.Signal = convergenceWatch
		convergence.Message = "The session may be entering diminishing returns; review whether another round is worth the cost."
		return convergence
	}
	convergence.Message = "No convergence pattern detected."
	return convergence
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
		SessionID:            s.id,
		State:                s.state,
		Verdict:              cloneStringPtr(s.verdict),
		OpenedAt:             s.openedAt,
		ClosedAt:             cloneTimePtr(s.closedAt),
		Budget:               s.budget,
		BudgetRemaining:      s.budget - len(s.rounds),
		MaxFindings:          s.maxFindings,
		ReviewContextSource:  s.reviewContextSource,
		ReviewContextPresent: s.reviewContext != "",
		RoundsUsed:           len(s.rounds),
		Reviewers:            []ReviewerInfo{s.reviewerInfo},
		Artifacts:            registeredArtifacts(s.artifacts),
		LastError:            cloneErrorInfo(s.lastError),
		ActiveRound:          jobStatusPtr(s.activeJob),
		LastRoundJob:         jobStatusPtr(s.lastRoundJob),
		Rounds:               rounds,
		Convergence:          s.convergence(),
	}
}

func (b *Broker) persistSessionLocked(s *session) error {
	return monitor.WriteStatus(monitor.StatusPath(s.dir), monitorSessionStatus(s.status()))
}

func (b *Broker) writeDecisionsLogLocked(s *session) error {
	return os.WriteFile(decisionsLogPath(s.dir), []byte(s.decisionsLogText()), 0o600)
}

func (b *Broker) appendEventLocked(s *session, event monitor.Event) error {
	return monitor.AppendEvent(monitor.EventsPath(s.dir), event)
}

func decisionsLogPath(sessionDir string) string {
	return filepath.Join(sessionDir, "decisions.md")
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

func (j *roundJob) status() RoundStatusResponse {
	return RoundStatusResponse{
		SessionID:   j.sessionID,
		RoundNumber: j.roundNumber,
		State:       j.state,
		Reviewer:    j.reviewerName,
		StartedAt:   j.startedAt,
		UpdatedAt:   j.updatedAt,
		CompletedAt: cloneTimePtr(j.completedAt),
		LogPath:     j.logPath,
		StatusPath:  j.statusPath,
		EventsPath:  j.eventsPath,
		Error:       cloneErrorInfo(j.errorInfo),
	}
}

func jobStatusPtr(job *roundJob) *RoundStatusResponse {
	if job == nil {
		return nil
	}
	status := job.status()
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
	if strings.HasPrefix(name, "_") {
		return fmt.Errorf("artifact name '%s' cannot begin with '_' (reserved for broker meta files in the snapshot directory)", name)
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

func effectiveReviewContext(sessionContext string, configContext string) (string, string) {
	if trimmed := strings.TrimSpace(sessionContext); trimmed != "" {
		return trimmed, reviewContextSession
	}
	if trimmed := strings.TrimSpace(configContext); trimmed != "" {
		return trimmed, reviewContextConfig
	}
	return "", reviewContextNone
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

func firstParsedOutput(round *round) (schema.ReviewOutput, bool) {
	if round == nil || len(round.results) == 0 {
		return schema.ReviewOutput{}, false
	}
	output, err := schema.ParseReviewOutput(round.results[0].Raw)
	if err != nil {
		return schema.ReviewOutput{}, false
	}
	return output, true
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

func registeredArtifacts(artifacts []Artifact) []RegisteredArtifact {
	out := make([]RegisteredArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, RegisteredArtifact{
			Name:       artifact.Name,
			SourcePath: artifact.Path,
			Inline:     artifact.Content != nil,
		})
	}
	return out
}

func reviewerInfo(spec ReviewerSpec) ReviewerInfo {
	return ReviewerInfo{
		Name:  spec.Name,
		Impl:  spec.Impl,
		Model: spec.Model,
	}
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
		Verdict:              cloneStringPtr(status.Verdict),
		OpenedAt:             status.OpenedAt,
		ClosedAt:             cloneTimePtr(status.ClosedAt),
		Budget:               status.Budget,
		BudgetRemaining:      status.BudgetRemaining,
		MaxFindings:          status.MaxFindings,
		ReviewContextSource:  status.ReviewContextSource,
		ReviewContextPresent: status.ReviewContextPresent,
		RoundsUsed:           status.RoundsUsed,
		Reviewers:            monitorReviewers(status.Reviewers),
		Artifacts:            monitorArtifacts(status.Artifacts),
		LastError:            monitorErrorInfo(status.LastError),
		ActiveRound:          monitorRoundJob(status.ActiveRound),
		LastRoundJob:         monitorRoundJob(status.LastRoundJob),
		Rounds:               monitorRounds(status.Rounds),
		Convergence:          monitorConvergence(status.Convergence),
	}
}

func monitorReviewers(reviewers []ReviewerInfo) []monitor.ReviewerInfo {
	out := make([]monitor.ReviewerInfo, 0, len(reviewers))
	for _, reviewer := range reviewers {
		out = append(out, monitor.ReviewerInfo{
			Name:  reviewer.Name,
			Impl:  reviewer.Impl,
			Model: reviewer.Model,
		})
	}
	return out
}

func monitorArtifacts(artifacts []RegisteredArtifact) []monitor.RegisteredArtifact {
	out := make([]monitor.RegisteredArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, monitor.RegisteredArtifact{
			Name:       artifact.Name,
			SourcePath: artifact.SourcePath,
			Inline:     artifact.Inline,
		})
	}
	return out
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

func monitorConvergence(convergence Convergence) monitor.Convergence {
	return monitor.Convergence{
		Signal:                      convergence.Signal,
		Message:                     convergence.Message,
		LatestBlockingFindings:      convergence.LatestBlockingFindings,
		PreviousBlockingFindings:    convergence.PreviousBlockingFindings,
		DeclinedOrDeferredDecisions: convergence.DeclinedOrDeferredDecisions,
		AcceptedDecisions:           convergence.AcceptedDecisions,
	}
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
		EventsPath:  status.EventsPath,
		Error:       monitorErrorInfo(status.Error),
	}
}

func monitorErrorInfo(info *ErrorInfo) *monitor.ErrorInfo {
	if info == nil {
		return nil
	}
	return &monitor.ErrorInfo{
		Code:       info.Code,
		Message:    info.Message,
		Details:    cloneDetails(info.Details),
		Retryable:  info.Retryable,
		NextAction: info.NextAction,
		At:         info.At,
	}
}

func cloneCollectedRoundResponse(response CollectedRoundResponse) CollectedRoundResponse {
	return CollectedRoundResponse{
		RoundNumber: response.RoundNumber,
		LogPath:     response.LogPath,
		Manifest:    cloneManifest(response.Manifest),
		Reviewers:   cloneResults(response.Reviewers),
		Convergence: response.Convergence,
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

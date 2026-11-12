package broker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/prompt"
	"github.com/michaelquigley/mercurius/internal/roundlog"
	"github.com/michaelquigley/mercurius/internal/runlock"
	"github.com/michaelquigley/mercurius/internal/schema"
	"github.com/michaelquigley/mercurius/internal/store"
)

var safeArtifactName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Broker serves the six session operations over the session directory. It holds
// no session state between calls: each operation resolves the configuration,
// reads what it needs from disk, acts, and writes.
type Broker struct {
	options Options
	pid     int
	// takeLock makes the one attempt at the runner lock that the dead-round check
	// uses. A field so a test can change a round's state in the gap between the
	// check's first read and its hold on the lock.
	takeLock func(logDestination string) (*runlock.Lock, error)
}

// New creates a broker.
func New(options Options) *Broker {
	return &Broker{
		options:  options,
		pid:      os.Getpid(),
		takeLock: runlock.TakeOnce,
	}
}

// OpenSession creates a session directory and its session.json.
func (b *Broker) OpenSession(ctx context.Context, _ OpenSessionRequest) (OpenSessionResponse, error) {
	if err := ctx.Err(); err != nil {
		return OpenSessionResponse{}, err
	}
	resolved, err := b.resolve()
	if err != nil {
		return OpenSessionResponse{}, err
	}
	logDestination := resolved.Project.LogDestination
	if err := config.EnsureLogDestination(logDestination); err != nil {
		return OpenSessionResponse{}, brokerError(CodeUserError, "invalid log destination", err, map[string]any{"log_destination": logDestination})
	}

	id, dir, err := createSessionDir(logDestination)
	if err != nil {
		return OpenSessionResponse{}, brokerError(CodeInternalError, "create session directory", err, nil)
	}
	openedAt := time.Now().UTC()
	if err := store.WriteSession(dir, store.Session{SessionID: id, OpenedAt: openedAt, State: store.SessionActive}); err != nil {
		_ = os.RemoveAll(dir)
		return OpenSessionResponse{}, brokerError(CodeInternalError, "write session", err, map[string]any{"session_dir": dir})
	}

	response := OpenSessionResponse{
		SessionID:            id,
		SessionDir:           dir,
		OpenedAt:             openedAt,
		MaxFindings:          resolved.Project.MaxFindings,
		ReviewContextPresent: strings.TrimSpace(resolved.Project.ReviewContext) != "",
		ReviewFocusPresent:   strings.TrimSpace(resolved.Project.ReviewFocus) != "",
	}
	if resolved.Reviewer != nil {
		info := reviewerInfoFromConfig(*resolved.Reviewer)
		response.Reviewer = &info
	}
	return response, nil
}

// StartReviewRound prepares a round and hands it, with the runner lock, to the
// launcher. Everything that can fail before the runner starts is checked here,
// and a failure leaves no round behind.
func (b *Broker) StartReviewRound(ctx context.Context, req StartRoundRequest) (StartReviewRoundResponse, error) {
	if err := ctx.Err(); err != nil {
		return StartReviewRoundResponse{}, err
	}
	resolved, err := b.resolve()
	if err != nil {
		return StartReviewRoundResponse{}, err
	}
	if resolved.Reviewer == nil {
		return StartReviewRoundResponse{}, brokerError(CodeUserError,
			fmt.Sprintf("no reviewer is configured; add a 'reviewer' block to '%s'", resolved.Project.ConfigPath),
			nil, map[string]any{"config_path": resolved.Project.ConfigPath})
	}
	logDestination := resolved.Project.LogDestination
	session, sessionDir, err := loadSession(logDestination, req.SessionID)
	if err != nil {
		return StartReviewRoundResponse{}, err
	}
	if session.State == store.SessionClosed {
		return StartReviewRoundResponse{}, brokerError(CodeConflict, "session is closed", nil, map[string]any{"session_id": req.SessionID})
	}
	artifacts, err := validateArtifacts(req.Artifacts)
	if err != nil {
		return StartReviewRoundResponse{}, brokerError(CodeUserError, "invalid artifacts", err, nil)
	}
	if b.options.Launcher == nil {
		return StartReviewRoundResponse{}, brokerError(CodeInternalError, "no round launcher configured", nil, nil)
	}

	lock, err := runlock.Take(logDestination)
	if errors.Is(err, runlock.ErrHeld) {
		return StartReviewRoundResponse{}, b.runningConflict(logDestination)
	}
	if err != nil {
		return StartReviewRoundResponse{}, brokerError(CodeInternalError, "take runner lock", err, map[string]any{"lock_path": store.LockPath(logDestination)})
	}
	handedOff := false
	defer func() {
		if !handedOff {
			lock.Release()
		}
	}()

	// the previous holder's round is the only one that can be marked running with
	// the lock free, since every round that ever ran was named in the note when it
	// started. it is read now, with the lock held, so the read is not stale.
	if note, err := runlock.ReadNote(logDestination); err == nil && store.ValidSessionID(note.SessionID) && note.RoundNumber > 0 {
		previous := store.RoundDir(store.SessionDir(logDestination, note.SessionID), note.RoundNumber)
		if store.RoundExists(previous) {
			if _, err := markLostLocked(previous); err != nil {
				return StartReviewRoundResponse{}, roundFileError(err, previous)
			}
		}
	}

	if err := removeLeftoverRounds(sessionDir); err != nil {
		return StartReviewRoundResponse{}, brokerError(CodeInternalError, "remove leftover round directory", err, map[string]any{"session_dir": sessionDir})
	}
	roundNumber, err := nextRoundNumber(sessionDir)
	if err != nil {
		return StartReviewRoundResponse{}, brokerError(CodeInternalError, "list rounds", err, map[string]any{"session_dir": sessionDir})
	}
	startedAt := time.Now().UTC()
	if err := lock.WriteNote(runlock.Note{
		SessionID:   session.SessionID,
		RoundNumber: roundNumber,
		ServerPID:   b.pid,
		StartedAt:   startedAt,
	}); err != nil {
		return StartReviewRoundResponse{}, brokerError(CodeInternalError, "write runner lock note", err, map[string]any{"lock_path": store.LockPath(logDestination)})
	}

	roundDir := store.RoundDir(sessionDir, roundNumber)
	reviewerCfg := *resolved.Reviewer
	if err := b.prepareRound(roundDir, session.SessionID, roundNumber, startedAt, resolved, reviewerCfg, artifacts); err != nil {
		_ = os.RemoveAll(roundDir)
		return StartReviewRoundResponse{}, err
	}
	if err := b.options.Launcher.Launch(roundDir, lock); err != nil {
		_ = os.RemoveAll(roundDir)
		return StartReviewRoundResponse{}, brokerError(CodeInternalError, "start round runner", err, map[string]any{"round_dir": roundDir})
	}
	handedOff = true

	return StartReviewRoundResponse{
		SessionID:      session.SessionID,
		RoundNumber:    roundNumber,
		State:          store.RoundRunning,
		Reviewer:       reviewerInfoFromConfig(reviewerCfg),
		StartedAt:      startedAt,
		StatusPath:     filepath.Join(roundDir, store.StateFileName),
		MonitorCommand: b.monitorCommand(session.SessionID),
		NextAction:     "tell the user this review is running; they can monitor it with the monitor_command and re-engage you when the round completes",
	}, nil
}

// prepareRound writes a round's inputs and its first state. _state.json is
// written last: a round exists once it does.
func (b *Broker) prepareRound(roundDir string, sessionID string, roundNumber int, startedAt time.Time, resolved *config.Resolved, reviewerCfg config.ReviewerConfig, artifacts []Artifact) error {
	if err := os.Mkdir(roundDir, 0o700); err != nil {
		return brokerError(CodeInternalError, "create round directory", err, map[string]any{"round_dir": roundDir})
	}
	manifest, promptArtifacts, err := snapshotArtifacts(roundDir, artifacts)
	if err != nil {
		return brokerError(CodeUserError, "snapshot artifacts", err, nil)
	}

	project := resolved.Project
	promptText, _ := prompt.Build(prompt.Request{
		Artifacts:        promptArtifacts,
		ReviewContext:    project.ReviewContext,
		ReviewFocus:      project.ReviewFocus,
		SettledDecisions: promptSettledDecisions(project.SettledDecisions),
		MaxFindings:      project.MaxFindings,
	})
	promptPath := filepath.Join(roundDir, store.PromptFileName)
	if err := os.WriteFile(promptPath, []byte(promptText), 0o600); err != nil {
		return brokerError(CodeInternalError, "write prompt log", err, map[string]any{"prompt_path": promptPath})
	}

	// the exact config bytes this round ran with, beside the rendered prompt.
	// _config.yaml is the input, _prompt.md is the output, so a guard present in
	// the config but absent from the prompt stays diagnosable from the round.
	configPath := filepath.Join(roundDir, store.ConfigFileName)
	if err := os.WriteFile(configPath, resolved.Raw, 0o600); err != nil {
		return brokerError(CodeInternalError, "write config log", err, map[string]any{"config_path": configPath})
	}

	runPath := filepath.Join(roundDir, store.RunFileName)
	if err := store.WriteJSON(runPath, store.Run{
		SessionID:            sessionID,
		RoundNumber:          roundNumber,
		OpenedAt:             startedAt,
		ServerPID:            b.pid,
		Reviewer:             runReviewer(reviewerCfg),
		MaxFindings:          project.MaxFindings,
		ReviewContextPresent: strings.TrimSpace(project.ReviewContext) != "",
		ReviewFocusPresent:   strings.TrimSpace(project.ReviewFocus) != "",
		Manifest:             manifest,
	}); err != nil {
		return brokerError(CodeInternalError, "write round inputs", err, map[string]any{"run_path": runPath})
	}

	if err := store.WriteState(roundDir, store.State{
		State:     store.RoundRunning,
		StartedAt: startedAt,
		UpdatedAt: startedAt,
	}); err != nil {
		return brokerError(CodeInternalError, "write round state", err, map[string]any{"state_path": filepath.Join(roundDir, store.StateFileName)})
	}
	return nil
}

// runningConflict builds the refusal for a launch while the runner lock is
// held, from the lock file's note and the named round's state. If the note
// cannot be read, the refusal is returned without them.
func (b *Broker) runningConflict(logDestination string) error {
	note, err := runlock.ReadNote(logDestination)
	if err != nil || !store.ValidSessionID(note.SessionID) || note.RoundNumber <= 0 {
		return brokerError(CodeConflict, "a review round is already running in this project", nil, map[string]any{
			"next_action": "do not start another round or open a new session to get around this; tell the user a round is already running and wait for it to finish",
		})
	}
	details := map[string]any{
		"running_session_id":     note.SessionID,
		"running_round_number":   note.RoundNumber,
		"started_by_this_server": note.ServerPID == b.pid,
		"monitor_command":        b.monitorCommand(note.SessionID),
		"next_action": fmt.Sprintf("do not start another round or open a new session to get around this; tell the user round %d of session '%s' is still running, wait for it to finish, then call collect_round on session '%s'",
			note.RoundNumber, note.SessionID, note.SessionID),
	}
	startedAt := note.StartedAt
	roundDir := store.RoundDir(store.SessionDir(logDestination, note.SessionID), note.RoundNumber)
	if state, err := store.ReadState(roundDir); err == nil {
		if !state.StartedAt.IsZero() {
			startedAt = state.StartedAt
		}
		if state.RunnerPID > 0 {
			details["runner_pid"] = state.RunnerPID
		}
	}
	if !startedAt.IsZero() {
		details["started_at"] = startedAt.UTC().Format(time.RFC3339)
		details["running_for"] = time.Since(startedAt).Round(time.Second).String()
	}
	return brokerError(CodeConflict,
		fmt.Sprintf("round %d of session '%s' is already running in this project", note.RoundNumber, note.SessionID),
		nil, details)
}

// CollectRound returns a completed round's outcome, or a failed round's stored
// failure as an error.
func (b *Broker) CollectRound(ctx context.Context, req CollectRoundRequest) (CollectedRoundResponse, error) {
	if err := ctx.Err(); err != nil {
		return CollectedRoundResponse{}, err
	}
	resolved, err := b.resolve()
	if err != nil {
		return CollectedRoundResponse{}, err
	}
	logDestination := resolved.Project.LogDestination
	_, sessionDir, err := loadSession(logDestination, req.SessionID)
	if err != nil {
		return CollectedRoundResponse{}, err
	}
	roundNumber := req.RoundNumber
	if roundNumber == 0 {
		latest, ok, err := latestRound(sessionDir)
		if err != nil {
			return CollectedRoundResponse{}, brokerError(CodeInternalError, "list rounds", err, map[string]any{"session_dir": sessionDir})
		}
		if !ok {
			return CollectedRoundResponse{}, brokerError(CodeNotFound, "session has no rounds", nil, map[string]any{"session_id": req.SessionID})
		}
		roundNumber = latest
	}
	roundDir := store.RoundDir(sessionDir, roundNumber)
	state, err := b.roundState(logDestination, req.SessionID, roundDir, roundNumber)
	if err != nil {
		return CollectedRoundResponse{}, err
	}
	statePath := filepath.Join(roundDir, store.StateFileName)

	switch state.State {
	case store.RoundRunning:
		return CollectedRoundResponse{}, brokerError(CodeConflict, "round is still running", nil, map[string]any{
			"session_id":      req.SessionID,
			"round_number":    roundNumber,
			"status_path":     statePath,
			"monitor_command": b.monitorCommand(req.SessionID),
		})
	case store.RoundFailed:
		return CollectedRoundResponse{}, storedFailure(state.Error, roundNumber, roundDir)
	case store.RoundCompleted:
	default:
		return CollectedRoundResponse{}, brokerError(CodeInternalError, fmt.Sprintf("round state '%s' is not known", state.State), nil, map[string]any{"state_path": statePath})
	}

	run, err := store.ReadRun(roundDir)
	if err != nil {
		return CollectedRoundResponse{}, roundFileError(err, filepath.Join(roundDir, store.RunFileName))
	}
	review, err := store.ReadReview(roundDir)
	if err != nil {
		return CollectedRoundResponse{}, roundFileError(err, filepath.Join(roundDir, store.ReviewFileName))
	}
	return CollectedRoundResponse{
		RoundNumber: roundNumber,
		LogPath:     filepath.Join(roundDir, store.RoundLogFileName),
		Manifest:    manifestFromStore(run.Manifest),
		Reviewers:   resultsFromStore(review.Reviewers),
	}, nil
}

// RecordRoundNotes records a completed round's commentary and decisions in
// _notes.json and its human twin _notes.md. The round log is not touched.
func (b *Broker) RecordRoundNotes(ctx context.Context, req RecordRoundNotesRequest) (RecordRoundNotesResponse, error) {
	if err := ctx.Err(); err != nil {
		return RecordRoundNotesResponse{}, err
	}
	resolved, err := b.resolve()
	if err != nil {
		return RecordRoundNotesResponse{}, err
	}
	_, sessionDir, err := loadSession(resolved.Project.LogDestination, req.SessionID)
	if err != nil {
		return RecordRoundNotesResponse{}, err
	}
	roundDir := store.RoundDir(sessionDir, req.RoundNumber)
	if req.RoundNumber <= 0 || !store.RoundExists(roundDir) {
		return RecordRoundNotesResponse{}, brokerError(CodeNotFound, "round not found", nil, map[string]any{"round_number": req.RoundNumber})
	}
	state, err := store.ReadState(roundDir)
	if err != nil {
		return RecordRoundNotesResponse{}, roundFileError(err, filepath.Join(roundDir, store.StateFileName))
	}
	switch state.State {
	case store.RoundRunning:
		return RecordRoundNotesResponse{}, brokerError(CodeConflict, "round is still running", nil, map[string]any{"round_number": req.RoundNumber})
	case store.RoundFailed:
		return RecordRoundNotesResponse{}, brokerError(CodeConflict, "round failed; it has no findings to decide and its record is the failure", nil, map[string]any{"round_number": req.RoundNumber})
	}
	if strings.TrimSpace(req.Commentary) == "" && len(req.Decisions) == 0 {
		return RecordRoundNotesResponse{}, brokerError(CodeUserError, "commentary and decisions are empty", nil, nil)
	}

	review, err := store.ReadReview(roundDir)
	if err != nil {
		return RecordRoundNotesResponse{}, roundFileError(err, filepath.Join(roundDir, store.ReviewFileName))
	}
	refs := refsFromReview(review)
	decisions := append([]Decision(nil), req.Decisions...)
	for _, decision := range decisions {
		if _, ok := refs[decision.Ref]; !ok {
			return RecordRoundNotesResponse{}, brokerError(CodeUserError, "decision ref is unknown", nil, map[string]any{
				"ref":        decision.Ref,
				"valid_refs": sortedRefs(refs),
			})
		}
		if !validDisposition(decision.Disposition) {
			return RecordRoundNotesResponse{}, brokerError(CodeUserError, "decision disposition is invalid", nil, map[string]any{"disposition": decision.Disposition})
		}
	}

	notesJSONPath := filepath.Join(roundDir, store.NotesJSONFileName)
	if err := store.WriteJSON(notesJSONPath, store.Notes{
		Commentary: req.Commentary,
		Decisions:  storeDecisions(decisions),
		RecordedAt: time.Now().UTC(),
	}); err != nil {
		return RecordRoundNotesResponse{}, brokerError(CodeInternalError, "write round notes", err, map[string]any{"notes_path": notesJSONPath})
	}
	notesPath := filepath.Join(roundDir, store.NotesFileName)
	if err := roundlog.WriteNotes(notesPath, req.Commentary, roundlogDecisions(decisions)); err != nil {
		return RecordRoundNotesResponse{}, brokerError(CodeInternalError, "write round notes", err, map[string]any{"notes_path": notesPath})
	}

	return RecordRoundNotesResponse{
		RoundNumber:        req.RoundNumber,
		LogPath:            notesPath,
		CommentaryRecorded: strings.TrimSpace(req.Commentary) != "",
		DecisionsRecorded:  len(decisions),
	}, nil
}

// CloseSession renders the synopsis from disk and marks the session closed.
func (b *Broker) CloseSession(ctx context.Context, req CloseSessionRequest) (CloseSessionResponse, error) {
	if err := ctx.Err(); err != nil {
		return CloseSessionResponse{}, err
	}
	resolved, err := b.resolve()
	if err != nil {
		return CloseSessionResponse{}, err
	}
	logDestination := resolved.Project.LogDestination
	session, sessionDir, err := loadSession(logDestination, req.SessionID)
	if err != nil {
		return CloseSessionResponse{}, err
	}
	if session.State == store.SessionClosed {
		return CloseSessionResponse{}, brokerError(CodeConflict, "session is already closed", nil, map[string]any{"session_id": req.SessionID})
	}
	if latest, ok, err := latestRound(sessionDir); err != nil {
		return CloseSessionResponse{}, brokerError(CodeInternalError, "list rounds", err, map[string]any{"session_dir": sessionDir})
	} else if ok {
		state, err := b.roundState(logDestination, req.SessionID, store.RoundDir(sessionDir, latest), latest)
		if err != nil {
			return CloseSessionResponse{}, err
		}
		if state.State == store.RoundRunning {
			return CloseSessionResponse{}, brokerError(CodeConflict, "a review round is still running", nil, map[string]any{
				"session_id":   req.SessionID,
				"round_number": latest,
			})
		}
	}

	closedAt := time.Now().UTC()
	entry, err := synopsisEntry(session, sessionDir, closedAt)
	if err != nil {
		return CloseSessionResponse{}, err
	}
	synopsisPath := filepath.Join(sessionDir, store.SynopsisFileName)
	if err := roundlog.WriteSynopsis(synopsisPath, entry); err != nil {
		return CloseSessionResponse{}, brokerError(CodeInternalError, "write session synopsis", err, map[string]any{"synopsis_path": synopsisPath})
	}
	session.State = store.SessionClosed
	session.ClosedAt = &closedAt
	if err := store.WriteSession(sessionDir, session); err != nil {
		return CloseSessionResponse{}, brokerError(CodeInternalError, "write session", err, map[string]any{"session_dir": sessionDir})
	}

	return CloseSessionResponse{
		SessionID:    session.SessionID,
		ClosedAt:     closedAt,
		SynopsisPath: synopsisPath,
	}, nil
}

// SessionStatus returns a read-only view of a session and every round in it.
func (b *Broker) SessionStatus(ctx context.Context, sessionID string) (SessionStatusResponse, error) {
	if err := ctx.Err(); err != nil {
		return SessionStatusResponse{}, err
	}
	resolved, err := b.resolve()
	if err != nil {
		return SessionStatusResponse{}, err
	}
	logDestination := resolved.Project.LogDestination
	session, sessionDir, err := loadSession(logDestination, sessionID)
	if err != nil {
		return SessionStatusResponse{}, err
	}
	numbers, err := store.Rounds(sessionDir)
	if err != nil {
		return SessionStatusResponse{}, brokerError(CodeInternalError, "list rounds", err, map[string]any{"session_dir": sessionDir})
	}

	rounds := make([]RoundStatus, 0, len(numbers))
	for i, n := range numbers {
		roundDir := store.RoundDir(sessionDir, n)
		var state store.State
		if i == len(numbers)-1 {
			state, err = b.roundState(logDestination, sessionID, roundDir, n)
		} else {
			state, err = readRoundState(roundDir)
		}
		if err != nil {
			return SessionStatusResponse{}, err
		}
		run, err := store.ReadRun(roundDir)
		if err != nil {
			return SessionStatusResponse{}, roundFileError(err, filepath.Join(roundDir, store.RunFileName))
		}
		status := RoundStatus{
			RoundNumber:    n,
			OpenedAt:       run.OpenedAt,
			State:          state.State,
			Reviewer:       reviewerInfoFromRun(run.Reviewer),
			MaxFindings:    run.MaxFindings,
			StatePath:      filepath.Join(roundDir, store.StateFileName),
			CompletedAt:    cloneTimePtr(state.CompletedAt),
			OutlivedServer: state.OutlivedServer,
			Error:          errorInfoFromStored(state.Error),
		}
		if state.State == store.RoundCompleted {
			status.LogPath = filepath.Join(roundDir, store.RoundLogFileName)
			notes, err := store.ReadNotes(roundDir)
			if err == nil {
				status.HasNotes = true
				status.DecisionCount = len(notes.Decisions)
			} else if !errors.Is(err, os.ErrNotExist) {
				return SessionStatusResponse{}, roundFileError(err, filepath.Join(roundDir, store.NotesJSONFileName))
			}
		}
		rounds = append(rounds, status)
	}

	return SessionStatusResponse{
		SessionID:  session.SessionID,
		State:      session.State,
		OpenedAt:   session.OpenedAt,
		ClosedAt:   cloneTimePtr(session.ClosedAt),
		RoundCount: len(rounds),
		Rounds:     rounds,
	}, nil
}

// roundState reads a round's state and, when it says running, runs the
// dead-round check. A missing round is not_found.
func (b *Broker) roundState(logDestination string, sessionID string, roundDir string, roundNumber int) (store.State, error) {
	if roundNumber <= 0 || !store.RoundExists(roundDir) {
		return store.State{}, brokerError(CodeNotFound, "round not found", nil, map[string]any{"session_id": sessionID, "round_number": roundNumber})
	}
	state, err := readRoundState(roundDir)
	if err != nil {
		return store.State{}, err
	}
	state, err = b.checkDeadRound(logDestination, roundDir, state)
	if err != nil {
		return store.State{}, roundFileError(err, filepath.Join(roundDir, store.StateFileName))
	}
	return state, nil
}

// checkDeadRound is the dead-round check every reader uses. A round marked
// running while the runner lock is free has no runner. The check makes one
// attempt at the lock; if it gets it, it re-reads the state, records the round
// as failed with reason runner_lost if the state still says running, and lets
// go. The re-read matters: the runner writes its final state and then exits, so
// the state read before the lock was taken can be stale.
func (b *Broker) checkDeadRound(logDestination string, roundDir string, state store.State) (store.State, error) {
	if state.State != store.RoundRunning {
		return state, nil
	}
	lock, err := b.takeLock(logDestination)
	if errors.Is(err, runlock.ErrHeld) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer lock.Release()
	return markLostLocked(roundDir)
}

// markLostLocked records a running round as failed with reason runner_lost.
// The caller must hold the runner lock. It reads the state itself, so a caller
// never decides from a read taken before the lock.
func markLostLocked(roundDir string) (store.State, error) {
	state, err := store.ReadState(roundDir)
	if err != nil {
		return state, err
	}
	if state.State != store.RoundRunning {
		return state, nil
	}
	now := time.Now().UTC()
	state.State = store.RoundFailed
	state.UpdatedAt = now
	state.CompletedAt = &now
	state.Error = store.NewStoredError(CodeReviewerFailed,
		"the round's runner is gone and the round did not finish",
		"", map[string]any{"reason": store.ReasonRunnerLost}, now)
	if err := store.WriteState(roundDir, state); err != nil {
		return state, err
	}
	return state, nil
}

func (b *Broker) resolve() (*config.Resolved, error) {
	resolved, err := config.Resolve(b.options.ConfigPath, b.options.UserConfigPath)
	if err != nil {
		details := map[string]any{}
		if path, pathErr := config.ProjectPath(b.options.ConfigPath); pathErr == nil {
			details["config_path"] = path
		}
		return nil, brokerError(CodeUserError, "read mercurius.yaml", err, details)
	}
	return resolved, nil
}

func (b *Broker) monitorCommand(sessionID string) string {
	path, err := config.ProjectPath(b.options.ConfigPath)
	if err != nil || path == "" {
		return fmt.Sprintf("mercurius monitor --session %s --wait", sessionID)
	}
	return fmt.Sprintf("mercurius monitor --config %s --session %s --wait", path, sessionID)
}

// loadSession checks a session id and reads its session.json. The id is checked
// before it is joined into a path, so nothing outside the log destination is
// ever read.
func loadSession(logDestination string, sessionID string) (store.Session, string, error) {
	if !store.ValidSessionID(sessionID) {
		return store.Session{}, "", brokerError(CodeNotFound, "session not found", nil, map[string]any{"session_id": sessionID})
	}
	sessionDir := store.SessionDir(logDestination, sessionID)
	session, err := store.ReadSession(sessionDir)
	switch {
	case err == nil:
		return session, sessionDir, nil
	case errors.Is(err, store.ErrNotSession):
		return store.Session{}, "", brokerError(CodeNotFound, "session not found", nil, map[string]any{
			"session_id": sessionID,
			"reason":     fmt.Sprintf("'%s' has no %s; the directory is not a session in this layout", sessionDir, store.SessionFileName),
		})
	case errors.Is(err, os.ErrNotExist):
		return store.Session{}, "", brokerError(CodeNotFound, "session not found", nil, map[string]any{"session_id": sessionID})
	default:
		return store.Session{}, "", roundFileError(err, filepath.Join(sessionDir, store.SessionFileName))
	}
}

func readRoundState(roundDir string) (store.State, error) {
	state, err := store.ReadState(roundDir)
	if err != nil {
		return store.State{}, roundFileError(err, filepath.Join(roundDir, store.StateFileName))
	}
	return state, nil
}

// roundFileError maps a failure reading or writing a machine file to an
// internal_error naming the file.
func roundFileError(err error, path string) error {
	var parseErr *store.ParseError
	if errors.As(err, &parseErr) {
		return brokerError(CodeInternalError, fmt.Sprintf("'%s' cannot be parsed", parseErr.Path), err, map[string]any{"path": parseErr.Path})
	}
	return brokerError(CodeInternalError, fmt.Sprintf("'%s' cannot be read or written", path), err, map[string]any{"path": path})
}

// storedFailure turns a failed round's stored error into the error
// collect_round returns.
func storedFailure(stored *store.StoredError, roundNumber int, roundDir string) error {
	details := map[string]any{}
	code := CodeInternalError
	message := "round failed with no recorded error"
	if stored != nil {
		code = stored.Code
		message = stored.Message
		for key, value := range stored.Details {
			details[key] = value
		}
	}
	details["round_number"] = roundNumber
	details["state_path"] = filepath.Join(roundDir, store.StateFileName)
	if rawPath := filepath.Join(roundDir, store.RawFileName); fileExists(rawPath) {
		details["raw_path"] = rawPath
	}
	return brokerError(code, message, nil, details)
}

func synopsisEntry(session store.Session, sessionDir string, closedAt time.Time) (roundlog.SynopsisEntry, error) {
	entry := roundlog.SynopsisEntry{
		SessionID: session.SessionID,
		OpenedAt:  session.OpenedAt,
		ClosedAt:  closedAt,
	}
	numbers, err := store.Rounds(sessionDir)
	if err != nil {
		return entry, brokerError(CodeInternalError, "list rounds", err, map[string]any{"session_dir": sessionDir})
	}
	for _, n := range numbers {
		round, err := synopsisRound(store.RoundDir(sessionDir, n), n)
		if err != nil {
			return entry, err
		}
		entry.Rounds = append(entry.Rounds, round)
	}
	return entry, nil
}

// synopsisRound assembles one round's synopsis material from its machine files.
// Markdown is never read back.
func synopsisRound(roundDir string, n int) (roundlog.SynopsisRound, error) {
	state, err := readRoundState(roundDir)
	if err != nil {
		return roundlog.SynopsisRound{}, err
	}
	run, err := store.ReadRun(roundDir)
	if err != nil {
		return roundlog.SynopsisRound{}, roundFileError(err, filepath.Join(roundDir, store.RunFileName))
	}
	sr := roundlog.SynopsisRound{
		Number:   n,
		OpenedAt: run.OpenedAt,
		State:    state.State,
		Reviewer: roundlog.SynopsisReviewer{
			Name:  run.Reviewer.Name,
			Impl:  run.Reviewer.Impl,
			Model: run.Reviewer.Model,
		},
		ReviewContextPresent: run.ReviewContextPresent,
		ReviewFocusPresent:   run.ReviewFocusPresent,
		OutlivedServer:       state.OutlivedServer,
	}
	if state.Error != nil {
		sr.Failure = &roundlog.SynopsisError{Code: state.Error.Code, Message: state.Error.Message}
	}
	if rawPath := filepath.Join(roundDir, store.RawFileName); fileExists(rawPath) {
		sr.RawPath = rawPath
	}
	if state.State != store.RoundCompleted {
		return sr, nil
	}

	sr.LogPath = filepath.Join(roundDir, store.RoundLogFileName)
	review, err := store.ReadReview(roundDir)
	if err != nil {
		return sr, roundFileError(err, filepath.Join(roundDir, store.ReviewFileName))
	}
	if len(review.Reviewers) > 0 {
		output, err := schema.ParseReviewOutput(review.Reviewers[0].Raw)
		if err != nil {
			sr.Unparseable = true
		} else {
			sr.Verdict = output.Verdict
			sr.ReviewerSummary = output.Summary
			sr.Concerns = append([]schema.Concern(nil), output.Concerns...)
			sr.Questions = append([]schema.Question(nil), output.Questions...)
			sr.AdvisoryNotes = append([]schema.AdvisoryNote(nil), output.AdvisoryNotes...)
		}
	}
	notes, err := store.ReadNotes(roundDir)
	if err == nil {
		sr.HasNotes = true
		sr.NotesPath = filepath.Join(roundDir, store.NotesFileName)
		sr.Commentary = notes.Commentary
		for _, d := range notes.Decisions {
			sr.Decisions = append(sr.Decisions, roundlog.Decision{Ref: d.Ref, Disposition: d.Disposition, Note: d.Note})
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return sr, roundFileError(err, filepath.Join(roundDir, store.NotesJSONFileName))
	}
	return sr, nil
}

// removeLeftoverRounds removes round directories with no _state.json: leftovers
// from a server that died while preparing a round. The caller holds the lock.
func removeLeftoverRounds(sessionDir string) error {
	dirs, err := store.RoundDirs(sessionDir)
	if err != nil {
		return err
	}
	for _, n := range dirs {
		roundDir := store.RoundDir(sessionDir, n)
		if !store.RoundExists(roundDir) {
			if err := os.RemoveAll(roundDir); err != nil {
				return err
			}
		}
	}
	return nil
}

// nextRoundNumber is one more than the highest round directory in the session.
// The project-wide lock serializes every round start, so there is no second
// allocator to race.
func nextRoundNumber(sessionDir string) (int, error) {
	dirs, err := store.RoundDirs(sessionDir)
	if err != nil {
		return 0, err
	}
	if len(dirs) == 0 {
		return 1, nil
	}
	return dirs[len(dirs)-1] + 1, nil
}

func latestRound(sessionDir string) (int, bool, error) {
	rounds, err := store.Rounds(sessionDir)
	if err != nil {
		return 0, false, err
	}
	if len(rounds) == 0 {
		return 0, false, nil
	}
	return rounds[len(rounds)-1], true, nil
}

func createSessionDir(logDestination string) (string, string, error) {
	for range 8 {
		id, err := newSessionID()
		if err != nil {
			return "", "", err
		}
		dir := store.SessionDir(logDestination, id)
		if err := os.Mkdir(dir, 0o700); err == nil {
			return id, dir, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", "", err
		}
	}
	return "", "", errors.New("could not allocate unique session id")
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

func snapshotArtifacts(roundDir string, artifacts []Artifact) ([]store.ManifestEntry, []prompt.Artifact, error) {
	var manifest []store.ManifestEntry
	var promptArtifacts []prompt.Artifact
	for _, artifact := range artifacts {
		content, err := os.ReadFile(artifact.Path)
		if err != nil {
			return nil, nil, err
		}
		snapshotPath := filepath.Join(roundDir, artifact.Name)
		if err := os.WriteFile(snapshotPath, content, 0o600); err != nil {
			return nil, nil, err
		}
		hash := sha256.Sum256(content)
		hashText := "sha256:" + hex.EncodeToString(hash[:])
		manifest = append(manifest, store.ManifestEntry{
			Name:         artifact.Name,
			SourcePath:   artifact.Path,
			SnapshotPath: snapshotPath,
			Size:         int64(len(content)),
			Hash:         hashText,
		})
		promptArtifacts = append(promptArtifacts, prompt.Artifact{
			Name:         artifact.Name,
			SourcePath:   artifact.Path,
			SnapshotPath: snapshotPath,
			Hash:         hashText,
			Content:      content,
		})
	}
	return manifest, promptArtifacts, nil
}

// promptSettledDecisions maps config guards into the prompt package's shape.
func promptSettledDecisions(in []config.SettledDecision) []prompt.SettledDecision {
	if len(in) == 0 {
		return nil
	}
	out := make([]prompt.SettledDecision, 0, len(in))
	for _, d := range in {
		out = append(out, prompt.SettledDecision{ID: d.ID, DoNotFlag: d.DoNotFlag})
	}
	return out
}

func validDisposition(disposition string) bool {
	return disposition == "fixed" || disposition == "rejected" || disposition == "deferred"
}

// refsFromReview recomputes the refs a round accepts for decisions from the raw
// reviewer output, through the same schema parse that produced them the first
// time. They are never stored.
func refsFromReview(review store.Review) map[string]struct{} {
	refs := map[string]struct{}{}
	for _, result := range review.Reviewers {
		output, err := schema.ParseReviewOutput(result.Raw)
		if err != nil {
			continue
		}
		for _, concern := range output.Concerns {
			refs[concern.ID] = struct{}{}
		}
		for _, question := range output.Questions {
			refs[question.ID] = struct{}{}
		}
		for _, note := range output.AdvisoryNotes {
			refs[note.ID] = struct{}{}
		}
	}
	return refs
}

// sortedRefs returns the ref ids known to a round, sorted, so an unknown-ref
// error can list the valid set and the operator does not have to grep the round
// log for the id format.
func sortedRefs(refs map[string]struct{}) []string {
	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	slices.Sort(out)
	return out
}

func runReviewer(cfg config.ReviewerConfig) store.RunReviewer {
	return store.RunReviewer{
		Name:       cfg.Name,
		Impl:       cfg.Impl,
		Model:      cfg.Model,
		BinaryPath: cfg.BinaryPath,
		ExtraArgs:  append([]string(nil), cfg.ExtraArgs...),
		Timeout:    store.Duration(cfg.Timeout),
	}
}

func reviewerInfoFromConfig(cfg config.ReviewerConfig) ReviewerInfo {
	return ReviewerInfo{
		Name:       cfg.Name,
		Impl:       cfg.Impl,
		Model:      cfg.Model,
		BinaryPath: cfg.BinaryPath,
		ExtraArgs:  append([]string(nil), cfg.ExtraArgs...),
		Timeout:    cfg.Timeout,
	}
}

func reviewerInfoFromRun(r store.RunReviewer) ReviewerInfo {
	return ReviewerInfo{
		Name:       r.Name,
		Impl:       r.Impl,
		Model:      r.Model,
		BinaryPath: r.BinaryPath,
		ExtraArgs:  append([]string(nil), r.ExtraArgs...),
		Timeout:    time.Duration(r.Timeout),
	}
}

func errorInfoFromStored(stored *store.StoredError) *ErrorInfo {
	if stored == nil {
		return nil
	}
	return &ErrorInfo{
		Code:    stored.Code,
		Message: stored.Message,
		Details: cloneDetails(stored.Details),
		At:      stored.At,
	}
}

func manifestFromStore(manifest []store.ManifestEntry) []ArtifactManifestEntry {
	out := make([]ArtifactManifestEntry, 0, len(manifest))
	for _, entry := range manifest {
		out = append(out, ArtifactManifestEntry{
			Name:         entry.Name,
			SourcePath:   entry.SourcePath,
			SnapshotPath: entry.SnapshotPath,
			Size:         entry.Size,
			Hash:         entry.Hash,
		})
	}
	return out
}

func resultsFromStore(results []store.ReviewerResult) []ReviewerResult {
	out := make([]ReviewerResult, 0, len(results))
	for _, result := range results {
		out = append(out, ReviewerResult{
			ReviewerName: result.ReviewerName,
			Raw:          append([]byte(nil), result.Raw...),
			UsageNotes:   result.UsageNotes,
		})
	}
	return out
}

func storeDecisions(decisions []Decision) []store.Decision {
	out := make([]store.Decision, 0, len(decisions))
	for _, d := range decisions {
		out = append(out, store.Decision{Ref: d.Ref, Disposition: d.Disposition, Note: d.Note})
	}
	return out
}

func roundlogDecisions(decisions []Decision) []roundlog.Decision {
	out := make([]roundlog.Decision, 0, len(decisions))
	for _, d := range decisions {
		out = append(out, roundlog.Decision{Ref: d.Ref, Disposition: d.Disposition, Note: d.Note})
	}
	return out
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

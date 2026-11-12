// Package runner executes one review round from its round directory. The round
// directory is the runner's entire input: the server writes the inputs
// (_run.json, _prompt.md, the snapshots) and the round's first state, and Run
// produces the outcome (_review.json and _round.md, or _raw.txt) and the final
// state. Run holds the runner lock for its whole life and releases it last.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/michaelquigley/df/dl"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/reviewer/claude"
	"github.com/michaelquigley/mercurius/internal/reviewer/codex"
	"github.com/michaelquigley/mercurius/internal/reviewer/dummy"
	"github.com/michaelquigley/mercurius/internal/reviewer/pi"
	"github.com/michaelquigley/mercurius/internal/roundlog"
	"github.com/michaelquigley/mercurius/internal/runlock"
	"github.com/michaelquigley/mercurius/internal/schema"
	"github.com/michaelquigley/mercurius/internal/store"
)

// ReviewerFactory builds the reviewer a round runs with from the resolved
// reviewer block recorded in its _run.json.
type ReviewerFactory func(cfg store.RunReviewer) (reviewer.Reviewer, error)

// Launcher is the seam the broker starts a round through. Launch takes
// ownership of the held lock: from a successful return the round's runner is
// responsible for releasing it. On error the lock is still the caller's.
type Launcher interface {
	Launch(roundDir string, lock *runlock.Lock) error
}

// InProcess runs rounds in goroutines inside the calling process. The reviewer
// factory lets tests supply reviewers as Go values.
type InProcess struct {
	build ReviewerFactory
	wg    sync.WaitGroup
}

// NewInProcess returns an in-process launcher. A nil factory uses
// BuildReviewer.
func NewInProcess(build ReviewerFactory) *InProcess {
	if build == nil {
		build = BuildReviewer
	}
	return &InProcess{build: build}
}

// Launch starts Run for the round in a goroutine.
func (l *InProcess) Launch(roundDir string, lock *runlock.Lock) error {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		if err := Run(roundDir, lock, l.build); err != nil {
			dl.Errorf("round '%s' did not record its outcome: %v", roundDir, err)
		}
	}()
	return nil
}

// Wait blocks until every round this launcher started has finished.
func (l *InProcess) Wait() {
	l.wg.Wait()
}

// Run executes the round in roundDir and writes its outcome and final state.
// It returns an error only when the final state itself could not be written.
// The lock is released when Run returns.
func Run(roundDir string, lock *runlock.Lock, build ReviewerFactory) error {
	defer lock.Release()
	if build == nil {
		build = BuildReviewer
	}
	r := &round{dir: roundDir}
	return r.execute(build)
}

type round struct {
	dir   string
	state store.State
	run   store.Run
}

func (r *round) execute(build ReviewerFactory) error {
	state, err := store.ReadState(r.dir)
	if err != nil {
		// without a readable state there is no started_at to keep; record the
		// failure on a fresh state rather than leave the round running.
		state = store.State{State: store.RoundRunning, StartedAt: time.Now().UTC()}
		r.state = state
		return r.fail(store.CodeInternalError, "read round state", err, nil)
	}
	r.state = state

	run, err := store.ReadRun(r.dir)
	if err != nil {
		return r.fail(store.CodeInternalError, "read round inputs", err, nil)
	}
	r.run = run

	promptText, err := os.ReadFile(filepath.Join(r.dir, store.PromptFileName))
	if err != nil {
		return r.fail(store.CodeInternalError, "read round prompt", err, nil)
	}

	impl, err := build(run.Reviewer)
	if err != nil {
		return r.fail(store.CodeInternalError, "build reviewer", err, map[string]any{"reviewer": run.Reviewer.Name})
	}

	timeout := time.Duration(run.Reviewer.Timeout)
	if timeout <= 0 {
		timeout = config.DefaultReviewerTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	resp, reviewErr := impl.Review(ctx, reviewer.ReviewRequest{
		Prompt:     string(promptText),
		Artifacts:  reviewerArtifacts(run.Manifest),
		Schema:     schema.ReviewOutputSchemaWithMaxFindings(run.MaxFindings),
		WorkingDir: r.dir,
		SessionMeta: reviewer.SessionMetadata{
			SessionID:   run.SessionID,
			RoundNumber: run.RoundNumber,
		},
	})
	// a reviewer that returned a valid review completes its round even when the
	// deadline has passed: a finished review is not thrown away because its
	// process failed to exit. the timeout is recorded only when the deadline
	// passed and Review returned an error.
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	cancel()

	if reviewErr != nil {
		details := map[string]any{"reviewer": run.Reviewer.Name}
		r.keepRaw(reviewer.CapturedOutput(reviewErr), details)
		message := "reviewer failed"
		if timedOut {
			details["reason"] = store.ReasonTimeout
			details["timeout"] = timeout.String()
			message = fmt.Sprintf("reviewer did not finish within '%s'", timeout)
		}
		return r.fail(store.CodeReviewerFailed, message, reviewErr, details)
	}

	output, err := schema.ParseReviewOutput(resp.Raw)
	if err != nil {
		details := map[string]any{"reviewer": run.Reviewer.Name}
		r.keepRaw(resp.Raw, details)
		return r.fail(store.CodeReviewerFailed, "reviewer output failed schema validation", err, details)
	}
	if err := schema.ValidateFindingLimit(output, run.MaxFindings); err != nil {
		details := map[string]any{
			"reviewer":     run.Reviewer.Name,
			"max_findings": run.MaxFindings,
			"findings":     schema.FindingCount(output),
			"concerns":     len(output.Concerns),
			"questions":    len(output.Questions),
		}
		r.keepRaw(resp.Raw, details)
		return r.fail(store.CodeReviewerFailed, "reviewer output exceeded max findings", err, details)
	}

	results := []store.ReviewerResult{{
		ReviewerName: run.Reviewer.Name,
		Raw:          append([]byte(nil), resp.Raw...),
		UsageNotes:   resp.UsageNotes,
	}}
	reviewPath := filepath.Join(r.dir, store.ReviewFileName)
	if err := store.WriteJSON(reviewPath, store.Review{Reviewers: results}); err != nil {
		return r.fail(store.CodeInternalError, "write review", err, map[string]any{"review_path": reviewPath})
	}
	logPath := filepath.Join(r.dir, store.RoundLogFileName)
	if err := roundlog.WriteInitial(logPath, roundlog.Entry{
		SessionID:   run.SessionID,
		RoundNumber: run.RoundNumber,
		OpenedAt:    run.OpenedAt,
		Verdict:     output.Verdict,
		PromptPath:  store.PromptFileName,
		Manifest:    roundlogManifest(run.Manifest),
		Reviewers:   roundlogReviewers(results),
	}); err != nil {
		return r.fail(store.CodeInternalError, "write round log", err, map[string]any{"log_path": logPath})
	}

	now := time.Now().UTC()
	r.state.State = store.RoundCompleted
	r.state.UpdatedAt = now
	r.state.CompletedAt = &now
	r.state.Error = nil
	return store.WriteState(r.dir, r.state)
}

// keepRaw writes reviewer output to _raw.txt and points the error details at
// it. Output is never stored inline in the error.
func (r *round) keepRaw(raw []byte, details map[string]any) {
	if len(raw) == 0 {
		return
	}
	rawPath := filepath.Join(r.dir, store.RawFileName)
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		details["raw_write_error"] = store.CapCause(err.Error())
		return
	}
	details["raw_path"] = rawPath
}

func (r *round) fail(code string, message string, err error, details map[string]any) error {
	now := time.Now().UTC()
	cause := ""
	if err != nil {
		cause = err.Error()
	}
	r.state.State = store.RoundFailed
	r.state.UpdatedAt = now
	r.state.CompletedAt = &now
	r.state.Error = store.NewStoredError(code, message, cause, details, now)
	return store.WriteState(r.dir, r.state)
}

func reviewerArtifacts(manifest []store.ManifestEntry) []reviewer.Artifact {
	out := make([]reviewer.Artifact, 0, len(manifest))
	for _, entry := range manifest {
		out = append(out, reviewer.Artifact{Name: entry.Name, Path: entry.SnapshotPath})
	}
	return out
}

func roundlogManifest(manifest []store.ManifestEntry) []roundlog.ArtifactManifestEntry {
	out := make([]roundlog.ArtifactManifestEntry, 0, len(manifest))
	for _, entry := range manifest {
		out = append(out, roundlog.ArtifactManifestEntry{
			Name:         entry.Name,
			SourcePath:   entry.SourcePath,
			SnapshotPath: entry.SnapshotPath,
			Size:         entry.Size,
			Hash:         entry.Hash,
		})
	}
	return out
}

func roundlogReviewers(results []store.ReviewerResult) []roundlog.ReviewerOutput {
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

// BuildReviewer builds the production reviewer for a resolved reviewer block.
func BuildReviewer(cfg store.RunReviewer) (reviewer.Reviewer, error) {
	switch cfg.Impl {
	case "codex":
		return codex.New(codex.Options{
			BinaryPath: cfg.BinaryPath,
			Model:      cfg.Model,
			ExtraArgs:  append([]string(nil), cfg.ExtraArgs...),
		}), nil
	case "claude":
		return claude.New(claude.Options{
			BinaryPath: cfg.BinaryPath,
			Model:      cfg.Model,
			ExtraArgs:  append([]string(nil), cfg.ExtraArgs...),
		}), nil
	case "pi":
		return pi.New(pi.Options{
			BinaryPath: cfg.BinaryPath,
			Model:      cfg.Model,
			ExtraArgs:  append([]string(nil), cfg.ExtraArgs...),
		}), nil
	case "dummy":
		return dummy.New(), nil
	default:
		return nil, fmt.Errorf("reviewer '%s': unknown impl '%s'", cfg.Name, cfg.Impl)
	}
}

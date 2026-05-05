package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/monitor"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/roundlog"
	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestSessionRoundsNotesPriorDecisionsAndClose(t *testing.T) {
	ctx := context.Background()
	design := writeArtifactFile(t, "design-v1")
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()})
	b := testBroker(t, r, 3)

	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: design}},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if open.Budget != 3 || open.BudgetRemaining != 3 || open.RoundsUsed != 0 {
		t.Fatalf("unexpected open budget metadata: %+v", open)
	}
	if len(open.Reviewers) != 1 || open.Reviewers[0].Name != "dummy" {
		t.Fatalf("unexpected open reviewers: %+v", open.Reviewers)
	}
	if len(open.Artifacts) != 1 || open.Artifacts[0].Name != "design" || open.Artifacts[0].SourcePath != design {
		t.Fatalf("unexpected open artifacts: %+v", open.Artifacts)
	}

	round1, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("review round 1: %v", err)
	}
	if round1.RoundNumber != 1 {
		t.Fatalf("round number = %d, want 1", round1.RoundNumber)
	}
	if _, err := os.Stat(round1.LogPath); err != nil {
		t.Fatalf("expected round log: %v", err)
	}
	assertRoundLogStructure(t, round1.LogPath, open.SessionID, 1, []string{"design"}, []string{"dummy"})
	assertSnapshot(t, round1.Manifest[0], "design-v1")

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Prompt == "" || !strings.Contains(req.Prompt, "flag unclear acceptance criteria.") {
		t.Fatal("expected assembled prompt with overrides")
	}
	if len(req.Schema) == 0 {
		t.Fatal("expected schema in reviewer request")
	}
	if req.Artifacts[0].Path != round1.Manifest[0].SnapshotPath {
		t.Fatal("expected reviewer artifact path to point at snapshot")
	}
	if req.SessionMeta.SessionID != open.SessionID || req.SessionMeta.RoundNumber != 1 {
		t.Fatalf("unexpected session metadata: %+v", req.SessionMeta)
	}

	notes, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "commentary",
		Decisions:   []Decision{{Ref: "C-1", Disposition: "accepted", Note: "fix accepted"}},
	})
	if err != nil {
		t.Fatalf("record notes: %v", err)
	}
	if !notes.CommentaryRecorded || notes.DecisionsRecorded != 1 {
		t.Fatalf("unexpected notes response: %+v", notes)
	}
	logContent := readFile(t, round1.LogPath)
	if !strings.Contains(logContent, "notes_recorded: true") || !strings.Contains(logContent, "- **accepted** (ref: C-1): fix accepted.") {
		t.Fatalf("notes not written:\n%s", logContent)
	}

	if err := os.WriteFile(design, []byte("design-v2"), 0o600); err != nil {
		t.Fatalf("edit artifact: %v", err)
	}
	r.push(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	round2, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("review round 2: %v", err)
	}
	assertSnapshot(t, round1.Manifest[0], "design-v1")
	assertSnapshot(t, round2.Manifest[0], "design-v2")
	if round1.Manifest[0].Hash == round2.Manifest[0].Hash {
		t.Fatal("expected distinct hashes after source edit")
	}

	reqs = r.requests()
	if got := reqs[1].SessionMeta.PriorDecisions; len(got) != 1 || got[0].Ref != "C-1" || got[0].RoundNumber != 1 {
		t.Fatalf("unexpected prior decisions: %+v", got)
	}

	status, err := b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RoundsUsed != 2 || !status.Rounds[0].HasNotes || status.Rounds[0].DecisionCount != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.BudgetRemaining != 1 || len(status.Reviewers) != 1 || len(status.Artifacts) != 1 || status.LastError != nil {
		t.Fatalf("unexpected status diagnostics: %+v", status)
	}

	closed, err := b.CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID, Verdict: "ready_to_build"})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Verdict != "ready_to_build" {
		t.Fatalf("unexpected close response: %+v", closed)
	}
	_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeSessionClosed)
}

func TestBudgetExhaustion(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := testBroker(t, r, 1)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID}); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeBudgetExhausted)
}

func TestAsyncReviewRoundLifecycle(t *testing.T) {
	ctx := context.Background()
	r := newBlockingReviewer(validReviewOutput("ready_to_build"))
	b := testBroker(t, nil, 2)
	b.reviewers["dummy"] = ReviewerSpec{Name: "dummy", Factory: func(string) reviewer.Reviewer { return r }}
	b.reviewerList = []ReviewerSpec{{Name: "dummy", Factory: func(string) reviewer.Reviewer { return r }}}
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	started, err := b.StartReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("start round: %v", err)
	}
	if started.State != roundStateRunning || started.RoundNumber != 1 || started.MonitorCommand == "" {
		t.Fatalf("start response = %+v", started)
	}
	waitStarted(t, r)

	status, err := b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status while running: %v", err)
	}
	if status.ActiveRound == nil || status.ActiveRound.State != roundStateRunning || status.BudgetRemaining != 2 {
		t.Fatalf("running status = %+v", status)
	}
	if _, err := os.Stat(started.StatusPath); err != nil {
		t.Fatalf("expected status file: %v", err)
	}
	fileStatus, err := monitor.ReadStatus(started.StatusPath)
	if err != nil {
		t.Fatalf("read monitor status: %v", err)
	}
	if fileStatus.ActiveRound == nil || fileStatus.ActiveRound.RoundNumber != 1 {
		t.Fatalf("file status = %+v", fileStatus)
	}

	_, err = b.StartReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeRoundInProgress)
	_, err = b.CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID, Verdict: "paused"})
	assertBrokerCode(t, err, CodeRoundInProgress)

	r.release()
	waitRoundState(t, b, open.SessionID, 1, roundStateCompleted)
	collected, err := b.CollectRound(ctx, CollectRoundRequest{SessionID: open.SessionID, RoundNumber: 1})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if collected.RoundNumber != 1 || collected.LogPath == "" {
		t.Fatalf("collected = %+v", collected)
	}
	status, err = b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status after completion: %v", err)
	}
	if status.ActiveRound != nil || status.LastRoundJob == nil || status.LastRoundJob.State != roundStateCompleted || status.BudgetRemaining != 1 {
		t.Fatalf("completed status = %+v", status)
	}
	events, err := monitor.ReadEvents(started.EventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !hasEvent(events, "round_started") || !hasEvent(events, "reviewer_started") || !hasEvent(events, "round_completed") {
		t.Fatalf("events = %+v", events)
	}
}

func TestReviewRoundContinuesAfterCallerContextExpires(t *testing.T) {
	r := newBlockingReviewer(validReviewOutput("ready_to_build"))
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		DefaultBudget:  2,
		Reviewers: []ReviewerSpec{{
			Name:    "dummy",
			Factory: func(string) reviewer.Reviewer { return r },
		}},
	})
	open, err := b.OpenSession(context.Background(), OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
		done <- err
	}()
	waitStarted(t, r)
	err = <-done
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("review round err = %v, want deadline", err)
	}

	r.release()
	waitRoundState(t, b, open.SessionID, 1, roundStateCompleted)
	collected, err := b.CollectRound(context.Background(), CollectRoundRequest{SessionID: open.SessionID, RoundNumber: 1})
	if err != nil {
		t.Fatalf("collect after caller timeout: %v", err)
	}
	if collected.RoundNumber != 1 {
		t.Fatalf("collected = %+v", collected)
	}
}

func TestAtomicFailuresReuseRoundAndBudget(t *testing.T) {
	tests := []struct {
		name string
		resp scriptedResponse
		code string
	}{
		{name: "reviewer error", resp: scriptedResponse{err: errors.New("boom")}, code: CodeReviewerFailed},
		{name: "malformed json", resp: scriptedResponse{raw: json.RawMessage(`{"verdict":`)}, code: CodeSchemaViolation},
		{name: "schema invalid", resp: scriptedResponse{raw: json.RawMessage(`{"not":"valid review output"}`)}, code: CodeSchemaViolation},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			r := newScriptedReviewer(test.resp, scriptedResponse{raw: validReviewOutput("ready_to_build")})
			b := testBroker(t, r, 2)
			open, err := b.OpenSession(ctx, OpenSessionRequest{
				Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
			})
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
			assertBrokerCode(t, err, test.code)
			if _, err := os.Stat(filepath.Join(open.SessionDir, "round-01.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected no round log, got err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(open.SessionDir, "snapshots", "round-01")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected snapshot cleanup, got err=%v", err)
			}
			status, err := b.SessionStatus(ctx, open.SessionID)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if status.RoundsUsed != 0 {
				t.Fatalf("rounds used = %d, want 0", status.RoundsUsed)
			}
			if status.BudgetRemaining != 2 {
				t.Fatalf("budget remaining = %d, want 2", status.BudgetRemaining)
			}
			if status.LastError == nil || status.LastError.Code != test.code || !status.LastError.Retryable {
				t.Fatalf("last error = %+v, want retryable %s", status.LastError, test.code)
			}

			round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
			if err != nil {
				t.Fatalf("subsequent round: %v", err)
			}
			if round.RoundNumber != 1 {
				t.Fatalf("round number after failure = %d, want 1", round.RoundNumber)
			}
			status, err = b.SessionStatus(ctx, open.SessionID)
			if err != nil {
				t.Fatalf("status after successful retry: %v", err)
			}
			if status.LastError != nil {
				t.Fatalf("last error after successful retry = %+v, want nil", status.LastError)
			}
		})
	}
}

func TestMaxFindingsFailsRoundAtomically(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithFindingCounts(t, 1, 1)}, scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		DefaultBudget:  2,
		MaxFindings:    1,
		Reviewers: []ReviewerSpec{{
			Name:    "dummy",
			Factory: func(string) reviewer.Reviewer { return r },
		}},
	})
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeSchemaViolation)
	if _, err := os.Stat(filepath.Join(open.SessionDir, "round-01.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no round log, got err=%v", err)
	}
	status, err := b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RoundsUsed != 0 || status.BudgetRemaining != 2 {
		t.Fatalf("status after max findings failure = %+v", status)
	}
	if status.LastError == nil || status.LastError.Code != CodeSchemaViolation {
		t.Fatalf("last error = %+v, want schema violation", status.LastError)
	}
	if status.LastError.Details["max_findings"] != 1 || status.LastError.Details["findings"] != 2 {
		t.Fatalf("last error details = %+v", status.LastError.Details)
	}

	round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("round after max findings failure: %v", err)
	}
	if round.RoundNumber != 1 {
		t.Fatalf("round number after max findings failure = %d, want 1", round.RoundNumber)
	}
	status, err = b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status after retry: %v", err)
	}
	if status.LastError != nil {
		t.Fatalf("last error after retry = %+v, want nil", status.LastError)
	}
}

func TestArtifactOverrideUpdatesOnlyAfterSuccessfulRound(t *testing.T) {
	ctx := context.Background()
	original := writeArtifactFile(t, "original")
	override := writeArtifactFile(t, "override")
	r := newScriptedReviewer(scriptedResponse{err: errors.New("fail override")}, scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := testBroker(t, r, 2)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: original}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	_, err = b.ReviewRound(ctx, ReviewRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: override}},
	})
	assertBrokerCode(t, err, CodeReviewerFailed)
	status, err := b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status after failed override: %v", err)
	}
	if status.LastError == nil || status.LastError.Code != CodeReviewerFailed {
		t.Fatalf("last error = %+v, want reviewer failure", status.LastError)
	}

	round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("round after failed override: %v", err)
	}
	if round.Manifest[0].SourcePath != original {
		t.Fatalf("expected original artifact after failed override, got %s", round.Manifest[0].SourcePath)
	}
	status, err = b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status after successful round: %v", err)
	}
	if status.LastError != nil {
		t.Fatalf("last error after successful round = %+v, want nil", status.LastError)
	}
}

func TestListReviewers(t *testing.T) {
	ctx := context.Background()
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		DefaultBudget:  1,
		Reviewers: []ReviewerSpec{
			{Name: "codex", Impl: "codex", Model: "gpt-test", Factory: func(string) reviewer.Reviewer { return newScriptedReviewer() }},
			{Name: "dummy", Impl: "dummy", Factory: func(string) reviewer.Reviewer { return newScriptedReviewer() }},
		},
	})

	response, err := b.ListReviewers(ctx)
	if err != nil {
		t.Fatalf("list reviewers: %v", err)
	}
	if len(response.Reviewers) != 2 {
		t.Fatalf("reviewers = %+v", response.Reviewers)
	}
	if response.Reviewers[0].Name != "codex" || response.Reviewers[0].Model != "gpt-test" {
		t.Fatalf("first reviewer = %+v", response.Reviewers[0])
	}
	if response.Reviewers[1].Name != "dummy" || response.Reviewers[1].Impl != "dummy" {
		t.Fatalf("second reviewer = %+v", response.Reviewers[1])
	}
}

func TestRecordRoundNotesValidationAndReplacement(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()})
	b := testBroker(t, r, 2)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("round: %v", err)
	}

	_, err = b.RecordRoundNotes(ctx, RecordRoundNotesRequest{SessionID: open.SessionID, RoundNumber: 1})
	assertBrokerCode(t, err, CodeEmptyNotes)
	_, err = b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Decisions:   []Decision{{Ref: "missing", Disposition: "accepted", Note: "nope"}},
	})
	assertBrokerCode(t, err, CodeUnknownRef)

	if _, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "first",
		Decisions:   []Decision{{Ref: "Q-1", Disposition: "deferred", Note: "later"}},
	}); err != nil {
		t.Fatalf("record first notes: %v", err)
	}
	if _, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "second",
	}); err != nil {
		t.Fatalf("replace notes: %v", err)
	}
	content := readFile(t, round.LogPath)
	if strings.Contains(content, "first") || strings.Contains(content, "deferred") {
		t.Fatalf("old notes remained:\n%s", content)
	}
	if !strings.Contains(content, "second") || !strings.Contains(content, "_no decisions recorded yet_") {
		t.Fatalf("replacement not written:\n%s", content)
	}
	if strings.Count(content, roundlog.NotesBeginMarker) != 1 || strings.Count(content, roundlog.NotesEndMarker) != 1 {
		t.Fatal("expected one notes region")
	}
}

func TestOpenSessionArtifactAndLogDestinationValidation(t *testing.T) {
	ctx := context.Background()
	validPath := writeArtifactFile(t, "content")

	tests := []struct {
		name      string
		artifacts []Artifact
	}{
		{name: "empty", artifacts: nil},
		{name: "duplicate", artifacts: []Artifact{{Name: "design", Path: validPath}, {Name: "design", Path: validPath}}},
		{name: "slash", artifacts: []Artifact{{Name: "bad/name", Path: validPath}}},
		{name: "dot", artifacts: []Artifact{{Name: ".", Path: validPath}}},
		{name: "non absolute", artifacts: []Artifact{{Name: "design", Path: "design.md"}}},
		{name: "missing", artifacts: []Artifact{{Name: "design", Path: filepath.Join(t.TempDir(), "missing.md")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := testBroker(t, newScriptedReviewer(), 1)
			_, err := b.OpenSession(ctx, OpenSessionRequest{Artifacts: test.artifacts})
			assertBrokerCode(t, err, CodeInvalidArtifacts)
		})
	}

	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "missing-parent", "reviews"),
		DefaultBudget:  1,
		Reviewers: []ReviewerSpec{{
			Name:    "dummy",
			Factory: func(string) reviewer.Reviewer { return newScriptedReviewer() },
		}},
	})
	_, err := b.OpenSession(ctx, OpenSessionRequest{Artifacts: []Artifact{{Name: "design", Path: validPath}}})
	assertBrokerCode(t, err, CodeInvalidLogDestination)
}

func TestInlineArtifactSnapshotAndPrompt(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := testBroker(t, r, 1)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "context", Path: "ignored-relative-path", Content: []byte("inline content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("round: %v", err)
	}
	if !round.Manifest[0].Inline || round.Manifest[0].SourcePath != "" {
		t.Fatalf("unexpected inline manifest: %+v", round.Manifest[0])
	}
	assertSnapshot(t, round.Manifest[0], "inline content")
	if !strings.Contains(readFile(t, round.LogPath), "| context | null |") {
		t.Fatal("expected null source path in log")
	}
	if !strings.Contains(r.requests()[0].Prompt, "Source path: inline") {
		t.Fatal("expected inline source path in prompt")
	}
}

func TestRoundLogWriteFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := testBroker(t, r, 2)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := os.Mkdir(filepath.Join(open.SessionDir, "round-01.md"), 0o700); err != nil {
		t.Fatalf("create log path directory: %v", err)
	}

	_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeInternalError)
	if _, err := os.Stat(filepath.Join(open.SessionDir, "snapshots", "round-01")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected snapshot cleanup, got err=%v", err)
	}
	status, err := b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RoundsUsed != 0 {
		t.Fatalf("rounds used = %d, want 0", status.RoundsUsed)
	}
}

type scriptedResponse struct {
	raw json.RawMessage
	err error
}

type scriptedReviewer struct {
	mu        sync.Mutex
	responses []scriptedResponse
	captured  []reviewer.ReviewRequest
}

func newScriptedReviewer(responses ...scriptedResponse) *scriptedReviewer {
	return &scriptedReviewer{responses: append([]scriptedResponse(nil), responses...)}
}

func (r *scriptedReviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return reviewer.ReviewResponse{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.captured = append(r.captured, cloneReviewerRequest(req))
	if len(r.responses) == 0 {
		return reviewer.ReviewResponse{Raw: validReviewOutput("ready_to_build"), UsageNotes: "scripted"}, nil
	}
	resp := r.responses[0]
	r.responses = r.responses[1:]
	if resp.err != nil {
		return reviewer.ReviewResponse{}, resp.err
	}
	return reviewer.ReviewResponse{Raw: append(json.RawMessage(nil), resp.raw...), UsageNotes: "scripted"}, nil
}

func (r *scriptedReviewer) push(response scriptedResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses = append(r.responses, response)
}

func (r *scriptedReviewer) requests() []reviewer.ReviewRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	requests := make([]reviewer.ReviewRequest, 0, len(r.captured))
	for _, req := range r.captured {
		requests = append(requests, cloneReviewerRequest(req))
	}
	return requests
}

type blockingReviewer struct {
	started  chan struct{}
	releaseC chan struct{}
	once     sync.Once
	raw      json.RawMessage
}

func newBlockingReviewer(raw json.RawMessage) *blockingReviewer {
	return &blockingReviewer{
		started:  make(chan struct{}),
		releaseC: make(chan struct{}),
		raw:      append(json.RawMessage(nil), raw...),
	}
}

func (r *blockingReviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.releaseC:
		return reviewer.ReviewResponse{Raw: append(json.RawMessage(nil), r.raw...), UsageNotes: "blocking"}, nil
	case <-ctx.Done():
		return reviewer.ReviewResponse{}, ctx.Err()
	}
}

func (r *blockingReviewer) release() {
	close(r.releaseC)
}

func waitStarted(t *testing.T, r *blockingReviewer) {
	t.Helper()
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not start")
	}
}

func waitRoundState(t *testing.T, b *Broker, sessionID string, roundNumber int, state string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		status, err := b.RoundStatus(context.Background(), RoundStatusRequest{SessionID: sessionID, RoundNumber: roundNumber})
		if err == nil && status.State == state {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("round did not reach state %s; last status=%+v err=%v", state, status, err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func hasEvent(events []monitor.Event, name string) bool {
	for _, event := range events {
		if event.Event == name {
			return true
		}
	}
	return false
}

func testBroker(t *testing.T, r *scriptedReviewer, budget int) *Broker {
	t.Helper()

	return New(Options{
		LogDestination:  filepath.Join(t.TempDir(), "reviews"),
		DefaultBudget:   budget,
		PromptOverrides: "flag unclear acceptance criteria.",
		Reviewers: []ReviewerSpec{{
			Name:    "dummy",
			Factory: func(string) reviewer.Reviewer { return r },
		}},
	})
}

func writeArtifactFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

func assertSnapshot(t *testing.T, manifest ArtifactManifestEntry, want string) {
	t.Helper()

	got := readFile(t, manifest.SnapshotPath)
	if got != want {
		t.Fatalf("snapshot content = %q, want %q", got, want)
	}
	if manifest.Size != int64(len(want)) {
		t.Fatalf("snapshot size = %d, want %d", manifest.Size, len(want))
	}
	if !strings.HasPrefix(manifest.Hash, "sha256:") {
		t.Fatalf("expected sha256 hash, got %s", manifest.Hash)
	}
}

func assertRoundLogStructure(t *testing.T, path string, sessionID string, roundNumber int, artifactNames []string, reviewerNames []string) {
	t.Helper()

	content := readFile(t, path)
	parts := strings.SplitN(content, "---\n", 3)
	if len(parts) != 3 {
		t.Fatalf("round log missing frontmatter:\n%s", content)
	}
	frontmatter := parseFrontmatter(parts[1])
	if frontmatter["session_id"] != sessionID {
		t.Fatalf("frontmatter session_id = %q, want %q", frontmatter["session_id"], sessionID)
	}
	if frontmatter["round_number"] != strconv.Itoa(roundNumber) {
		t.Fatalf("frontmatter round_number = %q, want %d", frontmatter["round_number"], roundNumber)
	}
	if frontmatter["notes_recorded"] != "false" {
		t.Fatalf("frontmatter notes_recorded = %q, want false", frontmatter["notes_recorded"])
	}
	if !strings.Contains(parts[2], "## Artifact manifest\n\n| name | source_path | snapshot_path | size | hash |") {
		t.Fatal("round log missing artifact manifest table")
	}
	for _, name := range artifactNames {
		if !strings.Contains(parts[2], "| "+name+" |") {
			t.Fatalf("round log missing artifact row for %q", name)
		}
	}
	for _, name := range reviewerNames {
		if !strings.Contains(parts[2], "### "+name+"\n\n**Usage notes:**") {
			t.Fatalf("round log missing reviewer block for %q", name)
		}
	}
	if strings.Count(parts[2], roundlog.NotesBeginMarker) != 1 || strings.Count(parts[2], roundlog.NotesEndMarker) != 1 {
		t.Fatal("round log must contain exactly one notes marker pair")
	}
}

func parseFrontmatter(frontmatter string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(frontmatter, "\n") {
		if strings.HasPrefix(line, " ") || !strings.Contains(line, ":") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	return values
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func assertBrokerCode(t *testing.T, err error, code string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	if got := ErrorCode(err); got != code {
		t.Fatalf("error code = %s, want %s; err=%v", got, code, err)
	}
}

func validReviewOutput(verdict string) json.RawMessage {
	raw, err := json.Marshal(schema.ReviewOutput{
		Verdict:       verdict,
		Summary:       "reviewed",
		Concerns:      []schema.Concern{},
		Questions:     []schema.Question{},
		ProposedDiffs: []schema.ProposedDiff{},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func reviewOutputWithRefs() json.RawMessage {
	suggestion := "clarify the work order"
	raw, err := json.Marshal(schema.ReviewOutput{
		Verdict: "needs_changes",
		Summary: "reviewed",
		Concerns: []schema.Concern{
			{ID: "C-1", Severity: "major", Location: "work-order:M3", Claim: "missing detail", Rationale: "implementation would diverge", Suggestion: &suggestion},
		},
		Questions: []schema.Question{
			{ID: "Q-1", Topic: "budget", WhyItBlocks: "cannot judge loop length"},
		},
		ProposedDiffs: []schema.ProposedDiff{},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func reviewOutputWithFindingCounts(t *testing.T, concernCount int, questionCount int) json.RawMessage {
	t.Helper()

	suggestion := "tighten the artifact"
	output := schema.ReviewOutput{
		Verdict:       "needs_changes",
		Summary:       "reviewed",
		Concerns:      make([]schema.Concern, 0, concernCount),
		Questions:     make([]schema.Question, 0, questionCount),
		ProposedDiffs: []schema.ProposedDiff{},
	}
	for i := 0; i < concernCount; i++ {
		output.Concerns = append(output.Concerns, schema.Concern{
			ID:         "C-" + strconv.Itoa(i+1),
			Severity:   "major",
			Location:   "design",
			Claim:      "missing detail",
			Rationale:  "implementation would diverge",
			Suggestion: &suggestion,
		})
	}
	for i := 0; i < questionCount; i++ {
		output.Questions = append(output.Questions, schema.Question{
			ID:          "Q-" + strconv.Itoa(i+1),
			Topic:       "scope",
			WhyItBlocks: "cannot decide implementation shape",
		})
	}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal review output: %v", err)
	}
	return raw
}

func cloneReviewerRequest(req reviewer.ReviewRequest) reviewer.ReviewRequest {
	req.Artifacts = append([]reviewer.Artifact(nil), req.Artifacts...)
	for i := range req.Artifacts {
		req.Artifacts[i].Content = append([]byte(nil), req.Artifacts[i].Content...)
	}
	req.Schema = append(json.RawMessage(nil), req.Schema...)
	req.SessionMeta.PriorDecisions = append([]reviewer.PriorDecision(nil), req.SessionMeta.PriorDecisions...)
	return req
}

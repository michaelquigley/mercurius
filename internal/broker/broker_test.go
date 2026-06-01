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

	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestSessionRoundsNotesAndClose(t *testing.T) {
	ctx := context.Background()
	design := writeArtifactFile(t, "design-v1")
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()})
	b := testBroker(t, r)

	open, err := b.OpenSession(ctx, OpenSessionRequest{ReviewFocus: "flag unclear acceptance criteria."})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if open.Reviewer.Name != "dummy" {
		t.Fatalf("unexpected reviewer: %+v", open.Reviewer)
	}
	if !open.ReviewFocusPresent {
		t.Fatal("expected review focus to be reported as present")
	}

	round1, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID:   open.SessionID,
		Artifacts:   []Artifact{{Name: "design", Path: design}},
		ReviewFocus: "flag unclear acceptance criteria.",
	})
	if err != nil {
		t.Fatalf("review round 1: %v", err)
	}
	if round1.RoundNumber != 1 {
		t.Fatalf("round number = %d, want 1", round1.RoundNumber)
	}
	if _, err := os.Stat(round1.LogPath); err != nil {
		t.Fatalf("expected round log: %v", err)
	}
	if filepath.Base(round1.LogPath) != roundLogName {
		t.Fatalf("expected log named %s, got %s", roundLogName, round1.LogPath)
	}
	assertRoundLogStructure(t, round1.LogPath, open.SessionID, 1, []string{"design"}, []string{"dummy"})
	assertSnapshot(t, round1.Manifest[0], "design-v1")

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Prompt == "" || !strings.Contains(req.Prompt, "flag unclear acceptance criteria.") {
		t.Fatal("expected assembled prompt with config focus")
	}
	if req.Artifacts[0].Path != round1.Manifest[0].SnapshotPath {
		t.Fatal("expected reviewer artifact path to point at snapshot")
	}
	if req.SessionMeta.SessionID != open.SessionID || req.SessionMeta.RoundNumber != 1 {
		t.Fatalf("unexpected session metadata: %+v", req.SessionMeta)
	}
	promptLogPath := filepath.Join(open.SessionDir, "round-01", roundPromptName)
	loggedPrompt := readFile(t, promptLogPath)
	if loggedPrompt != req.Prompt {
		t.Fatalf("logged prompt does not match prompt sent to reviewer")
	}

	notes, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "commentary",
		Decisions:   []Decision{{Ref: "C-1", Disposition: "fixed", Note: "fix landed"}},
	})
	if err != nil {
		t.Fatalf("record notes: %v", err)
	}
	if !notes.CommentaryRecorded || notes.DecisionsRecorded != 1 {
		t.Fatalf("unexpected notes response: %+v", notes)
	}
	notesPath := filepath.Join(open.SessionDir, "round-01", roundNotesName)
	notesContent := readFile(t, notesPath)
	if !strings.Contains(notesContent, "- **fixed** (ref: C-1): fix landed.") {
		t.Fatalf("notes not written:\n%s", notesContent)
	}
	// the immutable round log must remain untouched after notes recording.
	logContent := readFile(t, round1.LogPath)
	if strings.Contains(logContent, "fix landed") || strings.Contains(logContent, "notes_recorded") {
		t.Fatalf("round log should not carry notes content: %s", logContent)
	}
	if _, err := os.Stat(filepath.Join(open.SessionDir, "decisions.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a session-level decisions log must not be written; got err=%v", err)
	}

	if err := os.WriteFile(design, []byte("design-v2"), 0o600); err != nil {
		t.Fatalf("edit artifact: %v", err)
	}
	r.push(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	round2, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: design}},
	})
	if err != nil {
		t.Fatalf("review round 2: %v", err)
	}
	assertSnapshot(t, round1.Manifest[0], "design-v1")
	assertSnapshot(t, round2.Manifest[0], "design-v2")
	if round1.Manifest[0].Hash == round2.Manifest[0].Hash {
		t.Fatal("expected distinct hashes after source edit")
	}

	status, err := b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RoundCount != 2 || !status.Rounds[0].HasNotes || status.Rounds[0].DecisionCount != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.Reviewer.Name != "dummy" || status.LastError != nil {
		t.Fatalf("unexpected status diagnostics: %+v", status)
	}

	closed, err := b.CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.SessionID != open.SessionID || closed.ClosedAt.IsZero() {
		t.Fatalf("unexpected close response: %+v", closed)
	}
	if closed.SynopsisPath == "" {
		t.Fatal("close response did not include a synopsis path")
	}
	wantSynopsisPath := filepath.Join(open.SessionDir, "_synopsis.md")
	if closed.SynopsisPath != wantSynopsisPath {
		t.Fatalf("synopsis path = %q, want %q", closed.SynopsisPath, wantSynopsisPath)
	}
	synopsis := readFile(t, closed.SynopsisPath)
	for _, want := range []string{
		"session_id: " + open.SessionID,
		"round_count: 2",
		"## Round outcomes",
		"| 01 | ",
		"| 02 | ",
		"## Round detail",
		"### Round 01",
		"### Round 02",
		"**Decisions**",
		"- **fixed** (C-1): fix landed.",
		"**Commentary**",
		"> commentary",
	} {
		if !strings.Contains(synopsis, want) {
			t.Errorf("synopsis missing %q\n---\n%s", want, synopsis)
		}
	}
	if strings.Contains(synopsis, "_no decisions recorded_") {
		t.Fatalf("synopsis must render its own structure, not copy the notes-file placeholder")
	}
	_, err = startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: design}},
	})
	assertBrokerCode(t, err, CodeConflict)
}

func TestOpenSessionReportsRequestCalibrationPresence(t *testing.T) {
	ctx := context.Background()
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		Reviewer:       newScriptedReviewer(),
		ReviewerInfo:   ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})
	open, err := b.OpenSession(ctx, OpenSessionRequest{ReviewContext: "request context"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !open.ReviewContextPresent {
		t.Fatalf("expected review_context_present, got %+v", open)
	}
	if open.ReviewFocusPresent {
		t.Fatal("did not pass review_focus, so it must be reported absent")
	}
}

// TestSessionCalibrationIsPerRequest is the regression guard for the
// cross-talk bug where review_context cached at server startup leaked into
// later rounds even after mercurius.yaml had been edited. Calibration is now
// carried per round on StartRoundRequest (the MCP layer re-reads it from the
// YAML at the start of every round), so two rounds with different request
// values must produce prompts that contain only their own calibration.
func TestSessionCalibrationIsPerRequest(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(
		scriptedResponse{raw: validReviewOutput("ready_to_build")},
		scriptedResponse{raw: validReviewOutput("ready_to_build")},
	)
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		Reviewer:       r,
		ReviewerInfo:   ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})

	openA, err := b.OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	if _, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID:     openA.SessionID,
		Artifacts:     []Artifact{{Name: "design", Path: writeArtifactFile(t, "alpha")}},
		ReviewContext: "calibration-alpha",
		ReviewFocus:   "focus-alpha",
	}); err != nil {
		t.Fatalf("round A: %v", err)
	}
	promptA := readFile(t, filepath.Join(openA.SessionDir, "round-01", roundPromptName))
	if !strings.Contains(promptA, "calibration-alpha") || !strings.Contains(promptA, "focus-alpha") {
		t.Fatalf("prompt A missing alpha calibration:\n%s", promptA)
	}

	openB, err := b.OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	if _, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID:     openB.SessionID,
		Artifacts:     []Artifact{{Name: "design", Path: writeArtifactFile(t, "bravo")}},
		ReviewContext: "calibration-bravo",
		ReviewFocus:   "focus-bravo",
	}); err != nil {
		t.Fatalf("round B: %v", err)
	}
	promptB := readFile(t, filepath.Join(openB.SessionDir, "round-01", roundPromptName))
	if !strings.Contains(promptB, "calibration-bravo") || !strings.Contains(promptB, "focus-bravo") {
		t.Fatalf("prompt B missing bravo calibration:\n%s", promptB)
	}
	if strings.Contains(promptB, "calibration-alpha") || strings.Contains(promptB, "focus-alpha") {
		t.Fatalf("prompt B leaked alpha calibration:\n%s", promptB)
	}
}

func TestRoundSnapshotsRawConfigAndRendersGuards(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		Reviewer:       r,
		ReviewerInfo:   ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})

	open, err := b.OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	rawConfig := []byte("settled_decisions:\n  - id: recall-deferred\n    do_not_flag: the absence of a 'recall' concept\n")
	if _, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID:        open.SessionID,
		Artifacts:        []Artifact{{Name: "design", Path: writeArtifactFile(t, "design")}},
		SettledDecisions: []SettledDecision{{ID: "recall-deferred", DoNotFlag: "the absence of a 'recall' concept"}},
		RawConfig:        rawConfig,
	}); err != nil {
		t.Fatalf("round: %v", err)
	}

	// _config.yaml is the input snapshot, byte-identical to what the round ran with.
	configPath := filepath.Join(open.SessionDir, "round-01", roundConfigName)
	got := readFile(t, configPath)
	if got != string(rawConfig) {
		t.Fatalf("_config.yaml not byte-identical:\n--- got ---\n%s\n--- want ---\n%s", got, rawConfig)
	}

	// the guard's load-bearing text reaches the prompt; its operator-side id does not.
	prompt := readFile(t, filepath.Join(open.SessionDir, "round-01", roundPromptName))
	if !strings.Contains(prompt, "the absence of a 'recall' concept") {
		t.Fatalf("expected guard text in prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "recall-deferred") {
		t.Fatal("guard id must not be rendered into the prompt")
	}
}

func TestReviewerOutputDuplicateIdsRejected(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{
			name: "duplicate within concerns",
			raw: func() json.RawMessage {
				raw, err := json.Marshal(schema.ReviewOutput{
					Verdict: "needs_changes",
					Summary: "reviewed",
					Concerns: []schema.Concern{
						{ID: "C-1", Severity: "major", Location: "x", Claim: "a", Rationale: "b"},
						{ID: "C-1", Severity: "minor", Location: "y", Claim: "c", Rationale: "d"},
					},
					Questions:     []schema.Question{},
					AdvisoryNotes: []schema.AdvisoryNote{},
				})
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}(),
		},
		{
			name: "id reused across concerns and advisory_notes",
			raw: func() json.RawMessage {
				raw, err := json.Marshal(schema.ReviewOutput{
					Verdict: "needs_changes",
					Summary: "reviewed",
					Concerns: []schema.Concern{
						{ID: "C-1", Severity: "major", Location: "x", Claim: "a", Rationale: "b"},
					},
					Questions: []schema.Question{},
					AdvisoryNotes: []schema.AdvisoryNote{
						{ID: "C-1", Location: "y", Note: "polish", Rationale: "tone"},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newScriptedReviewer(scriptedResponse{raw: tc.raw})
			b := testBroker(t, r)
			open, err := b.OpenSession(ctx, OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, err = startAndCollectRound(t, b, ctx, StartRoundRequest{
				SessionID: open.SessionID,
				Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
			})
			assertBrokerCode(t, err, CodeReviewerFailed)
		})
	}
}

func TestAsyncReviewRoundLifecycle(t *testing.T) {
	ctx := context.Background()
	r := newBlockingReviewer(validReviewOutput("ready_to_build"))
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		Reviewer:       r,
		ReviewerInfo:   ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})
	open, err := b.OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	started, err := b.StartReviewRound(ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
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
	if status.ActiveRound == nil || status.ActiveRound.State != roundStateRunning {
		t.Fatalf("running status = %+v", status)
	}

	_, err = b.StartReviewRound(ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	assertBrokerCode(t, err, CodeConflict)
	_, err = b.CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeConflict)

	r.release()
	waitRoundCompleted(t, b, open.SessionID, 1)
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
	if status.ActiveRound != nil || status.RoundCount != 1 {
		t.Fatalf("completed status = %+v", status)
	}
}

func TestAtomicFailuresAllowRetry(t *testing.T) {
	tests := []struct {
		name string
		resp scriptedResponse
		code string
	}{
		{name: "reviewer error", resp: scriptedResponse{err: errors.New("boom")}, code: CodeReviewerFailed},
		{name: "malformed json", resp: scriptedResponse{raw: json.RawMessage(`{"verdict":`)}, code: CodeReviewerFailed},
		{name: "schema invalid", resp: scriptedResponse{raw: json.RawMessage(`{"not":"valid review output"}`)}, code: CodeReviewerFailed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			r := newScriptedReviewer(test.resp, scriptedResponse{raw: validReviewOutput("ready_to_build")})
			b := testBroker(t, r)
			open, err := b.OpenSession(ctx, OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			design := writeArtifactFile(t, "content")

			_, err = startAndCollectRound(t, b, ctx, StartRoundRequest{
				SessionID: open.SessionID,
				Artifacts: []Artifact{{Name: "design", Path: design}},
			})
			assertBrokerCode(t, err, test.code)
			if _, err := os.Stat(filepath.Join(open.SessionDir, "round-01")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected round dir cleanup, got err=%v", err)
			}
			status, err := b.SessionStatus(ctx, open.SessionID)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if status.RoundCount != 0 {
				t.Fatalf("round count = %d, want 0", status.RoundCount)
			}
			if status.LastError == nil || status.LastError.Code != test.code {
				t.Fatalf("last error = %+v, want %s", status.LastError, test.code)
			}

			round, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
				SessionID: open.SessionID,
				Artifacts: []Artifact{{Name: "design", Path: design}},
			})
			if err != nil {
				t.Fatalf("subsequent round: %v", err)
			}
			if round.RoundNumber != 1 {
				t.Fatalf("round number after failure = %d, want 1", round.RoundNumber)
			}
		})
	}
}

func TestMaxFindingsFailsRoundAtomically(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithFindingCounts(t, 1, 1)}, scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		MaxFindings:    1,
		Reviewer:       r,
		ReviewerInfo:   ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})
	open, err := b.OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	design := writeArtifactFile(t, "content")

	_, err = startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: design}},
	})
	assertBrokerCode(t, err, CodeReviewerFailed)
	if _, err := os.Stat(filepath.Join(open.SessionDir, "round-01")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected round dir cleanup, got err=%v", err)
	}

	round, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: design}},
	})
	if err != nil {
		t.Fatalf("round after max findings failure: %v", err)
	}
	if round.RoundNumber != 1 {
		t.Fatalf("round number after max findings failure = %d, want 1", round.RoundNumber)
	}
}

func TestRecordRoundNotesValidationAndReplacement(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()})
	b := testBroker(t, r)
	open, err := b.OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	}); err != nil {
		t.Fatalf("round: %v", err)
	}

	_, err = b.RecordRoundNotes(ctx, RecordRoundNotesRequest{SessionID: open.SessionID, RoundNumber: 1})
	assertBrokerCode(t, err, CodeUserError)
	_, err = b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Decisions:   []Decision{{Ref: "missing", Disposition: "fixed", Note: "nope"}},
	})
	assertBrokerCode(t, err, CodeUserError)

	if _, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "first",
		Decisions:   []Decision{{Ref: "Q-1", Disposition: "deferred", Note: "later"}},
	}); err != nil {
		t.Fatalf("record first notes: %v", err)
	}
	notesPath := filepath.Join(open.SessionDir, "round-01", roundNotesName)
	content := readFile(t, notesPath)
	if !strings.Contains(content, "first") || !strings.Contains(content, "deferred") {
		t.Fatalf("expected first notes:\n%s", content)
	}
	// re-recording rewrites the sibling notes file
	if _, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "second",
	}); err != nil {
		t.Fatalf("replace notes: %v", err)
	}
	content = readFile(t, notesPath)
	if strings.Contains(content, "first") || strings.Contains(content, "deferred") {
		t.Fatalf("old notes remained:\n%s", content)
	}
	if !strings.Contains(content, "second") || !strings.Contains(content, "_no decisions recorded_") {
		t.Fatalf("replacement not written:\n%s", content)
	}
}

func TestRecordRoundNotesRejectsLegacyAcceptedDisposition(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()})
	b := testBroker(t, r)
	open, err := b.OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := startAndCollectRound(t, b, ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	}); err != nil {
		t.Fatalf("round: %v", err)
	}
	_, err = b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "x",
		Decisions:   []Decision{{Ref: "C-1", Disposition: "accepted", Note: "should fail"}},
	})
	assertBrokerCode(t, err, CodeUserError)
}

func TestStartRoundArtifactValidation(t *testing.T) {
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
		{name: "underscore prefix", artifacts: []Artifact{{Name: "_secret", Path: validPath}}},
		{name: "non absolute", artifacts: []Artifact{{Name: "design", Path: "design.md"}}},
		{name: "missing", artifacts: []Artifact{{Name: "design", Path: filepath.Join(t.TempDir(), "missing.md")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := testBroker(t, newScriptedReviewer())
			open, err := b.OpenSession(ctx, OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, err = b.StartReviewRound(ctx, StartRoundRequest{
				SessionID: open.SessionID,
				Artifacts: test.artifacts,
			})
			assertBrokerCode(t, err, CodeUserError)
		})
	}
}

func TestOpenSessionRejectsBadLogDestination(t *testing.T) {
	ctx := context.Background()
	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "missing-parent", "reviews"),
		Reviewer:       newScriptedReviewer(),
		ReviewerInfo:   ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})
	_, err := b.OpenSession(ctx, OpenSessionRequest{})
	assertBrokerCode(t, err, CodeUserError)
}

func TestCloseSessionWritesSynopsisWithoutRounds(t *testing.T) {
	ctx := context.Background()
	b := testBroker(t, newScriptedReviewer())
	open, err := b.OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	closed, err := b.CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.SynopsisPath == "" {
		t.Fatal("expected a synopsis path")
	}
	synopsis := readFile(t, closed.SynopsisPath)
	for _, want := range []string{
		"round_count: 0",
		"# Session synopsis",
		"## Summary",
		"Session closed with no rounds.",
	} {
		if !strings.Contains(synopsis, want) {
			t.Errorf("empty-rounds synopsis missing %q\n---\n%s", want, synopsis)
		}
	}
	for _, unwanted := range []string{"## Round outcomes", "## Round detail"} {
		if strings.Contains(synopsis, unwanted) {
			t.Errorf("empty-rounds synopsis should not contain %q\n---\n%s", unwanted, synopsis)
		}
	}
}

func TestUnknownSessionReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	b := testBroker(t, newScriptedReviewer())
	_, err := b.SessionStatus(ctx, "s_missing")
	assertBrokerCode(t, err, CodeNotFound)
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

func startAndCollectRound(t *testing.T, b *Broker, ctx context.Context, req StartRoundRequest) (CollectedRoundResponse, error) {
	t.Helper()

	started, err := b.StartReviewRound(ctx, req)
	if err != nil {
		return CollectedRoundResponse{}, err
	}
	waitRoundTerminal(t, b, started.SessionID, started.RoundNumber)
	return b.CollectRound(ctx, CollectRoundRequest{
		SessionID:   started.SessionID,
		RoundNumber: started.RoundNumber,
	})
}

func waitRoundTerminal(t *testing.T, b *Broker, sessionID string, roundNumber int) {
	t.Helper()

	deadline := time.After(time.Second)
	for {
		// poll session status for the round's terminal state.
		status, err := b.SessionStatus(context.Background(), sessionID)
		if err == nil {
			if status.ActiveRound == nil {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("round did not reach terminal state; last status=%+v err=%v", status, err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitRoundCompleted(t *testing.T, b *Broker, sessionID string, roundNumber int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		status, err := b.SessionStatus(context.Background(), sessionID)
		if err == nil && status.ActiveRound == nil && status.RoundCount >= roundNumber {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("round did not complete; last status=%+v err=%v", status, err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func testBroker(t *testing.T, r reviewer.Reviewer) *Broker {
	t.Helper()

	return New(Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		Reviewer:       r,
		ReviewerInfo:   ReviewerInfo{Name: "dummy", Impl: "dummy"},
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
	if frontmatter["prompt_path"] != roundPromptName {
		t.Fatalf("frontmatter prompt_path = %q, want %q", frontmatter["prompt_path"], roundPromptName)
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
		AdvisoryNotes: []schema.AdvisoryNote{},
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
		AdvisoryNotes: []schema.AdvisoryNote{},
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
		AdvisoryNotes: []schema.AdvisoryNote{},
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
	return req
}

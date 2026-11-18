package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/runlock"
	"github.com/michaelquigley/mercurius/internal/runner"
	"github.com/michaelquigley/mercurius/internal/store"
)

// baseConfig is a project mercurius.yaml; the reviewer lives in the harness's
// user-level file, which defaults to baseUser.
const baseConfig = "log_destination: reviews\n"

const baseUser = `reviewer:
  name: dummy
  impl: dummy
`

// TestSessionLifecycleAcrossBrokers runs a whole arc with a new Broker value for
// every step, on the same directory, so nothing can pass by staying in memory.
// it also deletes the markdown the broker writes before the steps that would
// need it if anything read markdown back.
func TestSessionLifecycleAcrossBrokers(t *testing.T) {
	ctx := context.Background()
	rawReview := reviewOutputWithRefs()
	r := newScriptedReviewer(scriptedResponse{raw: rawReview})
	h := newHarness(t, r, baseConfig+"review_focus: flag unclear acceptance criteria.\n")
	design := writeArtifactFile(t, "design-v1")

	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if open.Reviewer == nil || open.Reviewer.Name != "dummy" {
		t.Fatalf("unexpected reviewer: %+v", open.Reviewer)
	}
	if !open.ReviewFocusPresent || open.ReviewContextPresent {
		t.Fatalf("unexpected presence flags: %+v", open)
	}
	session := readSessionFile(t, open.SessionDir)
	if session.SessionID != open.SessionID || session.State != store.SessionActive {
		t.Fatalf("session.json = %+v", session)
	}

	started, err := h.broker().StartReviewRound(ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: design}},
	})
	if err != nil {
		t.Fatalf("start round 1: %v", err)
	}
	if started.RoundNumber != 1 || started.State != store.RoundRunning || started.Reviewer.Name != "dummy" {
		t.Fatalf("start response = %+v", started)
	}
	round1Dir := store.RoundDir(open.SessionDir, 1)
	if started.StatusPath != filepath.Join(round1Dir, store.StateFileName) {
		t.Fatalf("status path = %q, want the round's _state.json", started.StatusPath)
	}
	h.waitDone(round1Dir)
	assertLockFree(t, h.logDest)

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if !strings.Contains(req.Prompt, "flag unclear acceptance criteria.") {
		t.Fatal("expected assembled prompt with config focus")
	}
	if req.SessionMeta.SessionID != open.SessionID || req.SessionMeta.RoundNumber != 1 {
		t.Fatalf("unexpected session metadata: %+v", req.SessionMeta)
	}
	if loggedPrompt := readFile(t, filepath.Join(round1Dir, store.PromptFileName)); loggedPrompt != req.Prompt {
		t.Fatal("logged prompt does not match prompt sent to reviewer")
	}
	if req.Artifacts[0].Path != filepath.Join(round1Dir, "design") {
		t.Fatalf("reviewer artifact path = %q, want the snapshot", req.Artifacts[0].Path)
	}
	assertRoundLogStructure(t, filepath.Join(round1Dir, store.RoundLogFileName), open.SessionID, 1, []string{"design"}, []string{"dummy"})

	if _, err := h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "first pass",
		Decisions:   []Decision{{Ref: "C-1", Disposition: "fixed", Note: "fix landed"}},
	}); err != nil {
		t.Fatalf("record first notes: %v", err)
	}
	if content := readFile(t, filepath.Join(round1Dir, store.NotesFileName)); !strings.Contains(content, "- **fixed** (ref: C-1): fix landed.") {
		t.Fatalf("notes not written:\n%s", content)
	}
	logContent := readFile(t, filepath.Join(round1Dir, store.RoundLogFileName))
	if strings.Contains(logContent, "fix landed") {
		t.Fatalf("round log should not carry notes content: %s", logContent)
	}
	var prettyReview bytes.Buffer
	if err := json.Indent(&prettyReview, rawReview, "", "  "); err != nil {
		t.Fatalf("indent review fixture: %v", err)
	}
	if !strings.Contains(logContent, "```json\n"+prettyReview.String()+"\n```") {
		t.Fatalf("round log does not preserve the reviewer's JSON order:\n%s", logContent)
	}

	// nothing may read the markdown back: delete it before collecting and
	// re-recording.
	removeFile(t, filepath.Join(round1Dir, store.RoundLogFileName))
	removeFile(t, filepath.Join(round1Dir, store.NotesFileName))

	collected, err := h.broker().CollectRound(ctx, CollectRoundRequest{SessionID: open.SessionID, RoundNumber: 1})
	if err != nil {
		t.Fatalf("collect round 1: %v", err)
	}
	if collected.RoundNumber != 1 || len(collected.Reviewers) != 1 || collected.Reviewers[0].ReviewerName != "dummy" {
		t.Fatalf("collected = %+v", collected)
	}
	var wantReview map[string]any
	if err := json.Unmarshal(rawReview, &wantReview); err != nil {
		t.Fatalf("decode review fixture: %v", err)
	}
	if !reflect.DeepEqual(collected.Reviewers[0].Raw, wantReview) {
		t.Fatalf("collected review = %#v, want %#v", collected.Reviewers[0].Raw, wantReview)
	}
	assertSnapshot(t, collected.Manifest[0], "design-v1")

	notes, err := h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "second pass commentary",
		Decisions: []Decision{
			{Ref: "C-1", Disposition: "fixed", Note: "fix landed"},
			{Ref: "Q-1", Disposition: "deferred", Note: "revisit later"},
		},
	})
	if err != nil {
		t.Fatalf("re-record notes: %v", err)
	}
	if !notes.CommentaryRecorded || notes.DecisionsRecorded != 2 {
		t.Fatalf("unexpected notes response: %+v", notes)
	}
	removeFile(t, filepath.Join(round1Dir, store.NotesFileName))

	if err := os.WriteFile(design, []byte("design-v2"), 0o600); err != nil {
		t.Fatalf("edit artifact: %v", err)
	}
	r.push(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	round2, err := h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: design}})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if round2.RoundNumber != 2 {
		t.Fatalf("round 2 number = %d", round2.RoundNumber)
	}
	assertSnapshot(t, collected.Manifest[0], "design-v1")
	assertSnapshot(t, round2.Manifest[0], "design-v2")

	status, err := h.broker().SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RoundCount != 2 || !status.Rounds[0].HasNotes || status.Rounds[0].DecisionCount != 2 {
		t.Fatalf("unexpected status: %+v", status)
	}
	for _, round := range status.Rounds {
		if round.State != store.RoundCompleted || round.Reviewer.Name != "dummy" || round.MaxFindings != 6 || round.Error != nil {
			t.Fatalf("unexpected round status: %+v", round)
		}
	}

	closed, err := h.broker().CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.SynopsisPath != filepath.Join(open.SessionDir, store.SynopsisFileName) {
		t.Fatalf("synopsis path = %q", closed.SynopsisPath)
	}
	synopsis := readFile(t, closed.SynopsisPath)
	for _, want := range []string{
		"session_id: " + open.SessionID,
		"round_count: 2",
		"| 01 | ",
		"| 02 | ",
		"### Round 01",
		"### Round 02",
		"- **fixed** (C-1): fix landed.",
		"- **deferred** (Q-1): revisit later.",
		"> second pass commentary",
		"missing detail",
		"budget",
		"the design needs a detail and a budget decision",
	} {
		if !strings.Contains(synopsis, want) {
			t.Errorf("synopsis missing %q\n---\n%s", want, synopsis)
		}
	}
	if session := readSessionFile(t, open.SessionDir); session.State != store.SessionClosed || session.ClosedAt == nil {
		t.Fatalf("session.json after close = %+v", session)
	}

	_, err = h.broker().StartReviewRound(ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: design}},
	})
	assertBrokerCode(t, err, CodeConflict)
}

// calibration, the reviewer, and max_findings are resolved per round and
// recorded on it; _config.yaml is the exact bytes the round ran with. the
// user-level file is edited between the two rounds of one session, and the
// second round's _run.json holds the new reviewer.
func TestConfigurationIsResolvedPerRound(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig+`review_context: calibration-alpha
settled_decisions:
  - id: blank
    do_not_flag: ""
`)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	artifact := []Artifact{{Name: "design", Path: writeArtifactFile(t, "design")}}
	if _, err := h.startAndCollect(open.SessionID, artifact); err != nil {
		t.Fatalf("round 1: %v", err)
	}

	edited := `log_destination: reviews
max_findings: 3
review_context: calibration-bravo
settled_decisions:
  - id: recall-deferred
    do_not_flag: the absence of a 'recall' concept
`
	h.writeConfig(edited)
	h.writeUserConfig("reviewer:\n  name: second\n  impl: dummy\n  model: m-2\n  timeout: 90s\n")
	if _, err := h.startAndCollect(open.SessionID, artifact); err != nil {
		t.Fatalf("round 2: %v", err)
	}

	round1 := store.RoundDir(open.SessionDir, 1)
	round2 := store.RoundDir(open.SessionDir, 2)
	prompt1 := readFile(t, filepath.Join(round1, store.PromptFileName))
	if !strings.Contains(prompt1, "calibration-alpha") {
		t.Fatalf("round 1 prompt missing its calibration:\n%s", prompt1)
	}
	if strings.Contains(prompt1, "## Settled decisions") {
		t.Fatalf("round 1 prompt rendered the blank guard:\n%s", prompt1)
	}
	prompt2 := readFile(t, filepath.Join(round2, store.PromptFileName))
	if !strings.Contains(prompt2, "calibration-bravo") || strings.Contains(prompt2, "calibration-alpha") {
		t.Fatalf("round 2 prompt has the wrong calibration:\n%s", prompt2)
	}
	if !strings.Contains(prompt2, "the absence of a 'recall' concept") || strings.Contains(prompt2, "recall-deferred") {
		t.Fatalf("round 2 prompt must render the guard text and not its id:\n%s", prompt2)
	}
	if got := readFile(t, filepath.Join(round2, store.ConfigFileName)); got != edited {
		t.Fatalf("_config.yaml not byte-identical:\n%s", got)
	}

	run1, err := store.ReadRun(round1)
	if err != nil {
		t.Fatalf("read run 1: %v", err)
	}
	run2, err := store.ReadRun(round2)
	if err != nil {
		t.Fatalf("read run 2: %v", err)
	}
	if run1.Reviewer.Name != "dummy" || run1.MaxFindings != 6 || time.Duration(run1.Reviewer.Timeout) != 20*time.Minute {
		t.Fatalf("round 1 inputs = %+v", run1)
	}
	if run2.Reviewer.Name != "second" || run2.Reviewer.Model != "m-2" || run2.MaxFindings != 3 || time.Duration(run2.Reviewer.Timeout) != 90*time.Second {
		t.Fatalf("round 2 inputs = %+v", run2)
	}
	if run1.ReviewerSource != config.SourceUserConfig || run2.ReviewerSource != config.SourceUserConfig {
		t.Fatalf("reviewer sources = %q, %q, want the user config", run1.ReviewerSource, run2.ReviewerSource)
	}
	if !run2.ReviewContextPresent || run2.ReviewFocusPresent {
		t.Fatalf("round 2 presence = %+v", run2)
	}
	if run2.ServerPID != os.Getpid() || run2.SessionID != open.SessionID || run2.RoundNumber != 2 {
		t.Fatalf("round 2 identity = %+v", run2)
	}
}

func TestOpenSessionReportsCurrentConfig(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), "log_destination: reviews\nreview_context: present\n")
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if open.Reviewer == nil || open.Reviewer.Name != "dummy" || open.Reviewer.Source != config.SourceUserConfig {
		t.Fatalf("reviewer = %+v, want the user config's", open.Reviewer)
	}
	if !open.ReviewContextPresent || open.ReviewFocusPresent || open.MaxFindings != 6 {
		t.Fatalf("open = %+v", open)
	}
}

// neither file names a reviewer: open_session succeeds and reports none, and
// start_review_round refuses with user_error naming the user-level path.
func TestNoReviewerAnywhere(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	removeFile(t, h.userPath)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open without a reviewer must succeed: %v", err)
	}
	if open.Reviewer != nil {
		t.Fatalf("reviewer = %+v, want none", open.Reviewer)
	}
	_, err = h.broker().StartReviewRound(ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "x")}},
	})
	assertBrokerCode(t, err, CodeUserError)
	if !strings.Contains(err.Error(), h.userPath) {
		t.Fatalf("expected the error to name the user-level path '%s', got %v", h.userPath, err)
	}
	assertNoRounds(t, open.SessionDir)
}

// a plain reviewer block in the project file is refused by every tool with a
// user_error naming the user-level path, and does not mention the override key.
func TestProjectReviewerBlockIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig+baseUser)
	_, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	assertBrokerCode(t, err, CodeUserError)
	if !strings.Contains(err.Error(), h.userPath) || !strings.Contains(err.Error(), "remove the 'reviewer' block") {
		t.Fatalf("expected the error to name '%s' and say to remove the block, got %v", h.userPath, err)
	}
	if strings.Contains(err.Error(), "override") {
		t.Fatalf("the refusal must not mention the override key: %v", err)
	}
	var be *Error
	errors.As(err, &be)
	if be.Details["config_path"] != h.configPath {
		t.Fatalf("the project file is the one to fix: %+v", be.Details)
	}
}

// a broken user-level file is a user_error that names it, not the project file.
func TestBrokenUserConfigNamesTheUserFile(t *testing.T) {
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	h.writeUserConfig("this: is: not: valid: yaml\n\t::broken")
	_, err := h.broker().OpenSession(context.Background(), OpenSessionRequest{})
	assertBrokerCode(t, err, CodeUserError)
	var be *Error
	errors.As(err, &be)
	if be.Message != "read the user-level config" || be.Details["user_config_path"] != h.userPath {
		t.Fatalf("error = %+v", be)
	}
}

// a project override is said everywhere the reviewer is named: open_session,
// the launch response and its next_action, _run.json, session_status, and the
// synopsis. when the block comes out, the next round is back on the
// installation's reviewer.
func TestProjectOverrideIsReportedEverywhere(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig+"reviewer_override:\n  name: second-opinion\n  impl: dummy\n")
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if open.Reviewer == nil || open.Reviewer.Name != "second-opinion" || open.Reviewer.Source != config.SourceProjectOverride {
		t.Fatalf("open reviewer = %+v", open.Reviewer)
	}
	artifact := []Artifact{{Name: "design", Path: writeArtifactFile(t, "x")}}
	started, err := h.broker().StartReviewRound(ctx, StartRoundRequest{SessionID: open.SessionID, Artifacts: artifact})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.Reviewer.Source != config.SourceProjectOverride || !strings.Contains(started.NextAction, "tell the user") || !strings.Contains(started.NextAction, "reviewer_override") {
		t.Fatalf("start must report the override and tell the agent to say so: %+v", started)
	}
	h.waitDone(filepath.Dir(started.StatusPath))

	h.writeConfig(baseConfig)
	round2, err := h.startAndCollect(open.SessionID, artifact)
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if round2.RoundNumber != 2 {
		t.Fatalf("round 2 = %+v", round2)
	}
	run1, _ := store.ReadRun(store.RoundDir(open.SessionDir, 1))
	run2, _ := store.ReadRun(store.RoundDir(open.SessionDir, 2))
	if run1.ReviewerSource != config.SourceProjectOverride || run1.Reviewer.Name != "second-opinion" {
		t.Fatalf("round 1 _run.json = %+v", run1)
	}
	if run2.ReviewerSource != config.SourceUserConfig || run2.Reviewer.Name != "dummy" {
		t.Fatalf("round 2 must be back on the installation's reviewer: %+v", run2)
	}

	status, err := h.broker().SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Rounds[0].Reviewer.Source != config.SourceProjectOverride || status.Rounds[1].Reviewer.Source != config.SourceUserConfig {
		t.Fatalf("status sources = %+v", status.Rounds)
	}
	closed, err := h.broker().CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	synopsis := readFile(t, closed.SynopsisPath)
	for _, want := range []string{"| 01 | ", "second-opinion (override)", "from the project override", "| 02 | ", "from the user config"} {
		if !strings.Contains(synopsis, want) {
			t.Errorf("synopsis missing %q\n%s", want, synopsis)
		}
	}
}

func TestMissingConfigIsUserErrorNamingPath(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	removeFile(t, h.configPath)
	_, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	assertBrokerCode(t, err, CodeUserError)
	var be *Error
	if !errors.As(err, &be) || be.Details["config_path"] != h.configPath || !strings.Contains(err.Error(), h.configPath) {
		t.Fatalf("expected the error to name '%s', got %v (details %+v)", h.configPath, err, be.Details)
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
			raw: json.RawMessage(`{
				"verdict": "needs_changes",
				"summary": "reviewed",
				"concerns": [
					{"id": "C-1", "severity": "major", "location": "x", "claim": "a", "rationale": "b", "suggestion": null},
					{"id": "C-1", "severity": "minor", "location": "y", "claim": "c", "rationale": "d", "suggestion": null}
				],
				"questions": [],
				"advisory_notes": []
			}`),
		},
		{
			name: "id reused across concerns and advisory_notes",
			raw: json.RawMessage(`{
				"verdict": "needs_changes",
				"summary": "reviewed",
				"concerns": [
					{"id": "C-1", "severity": "major", "location": "x", "claim": "a", "rationale": "b", "suggestion": null}
				],
				"questions": [],
				"advisory_notes": [
					{"id": "C-1", "location": "y", "note": "polish", "rationale": "tone", "suggestion": null}
				]
			}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, newScriptedReviewer(scriptedResponse{raw: tc.raw}), baseConfig)
			open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, err = h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}})
			assertBrokerCode(t, err, CodeReviewerFailed)
			cause, _ := ErrorInfoFrom(err).Details["cause"].(string)
			if !strings.Contains(cause, "review output reuses id 'C-1'") {
				t.Fatalf("expected duplicate id failure, got '%s'", cause)
			}
		})
	}
}

// a round that fails keeps its directory, its state, and its raw output; the
// retry takes the next number; and collect_round answers the failure from disk
// through a new broker.
func TestFailedRoundStaysOnDisk(t *testing.T) {
	const duplicateSummary = `{"verdict":"ready_to_build","summary":"first","summary":"second","concerns":[],"questions":[],"advisory_notes":[]}`
	tests := []struct {
		name    string
		resp    scriptedResponse
		message string
		wantRaw string
		cause   string
	}{
		{
			name:    "reviewer error carrying output",
			resp:    scriptedResponse{err: &reviewer.OutputError{Err: errors.New("boom"), Output: []byte("partial output before boom")}},
			message: "reviewer failed",
			wantRaw: "partial output before boom",
		},
		{
			name:    "reviewer error without output",
			resp:    scriptedResponse{err: errors.New("boom")},
			message: "reviewer failed",
		},
		{
			name:    "malformed json",
			resp:    scriptedResponse{raw: json.RawMessage(`{"verdict":`)},
			message: "reviewer output failed schema validation",
			wantRaw: `{"verdict":`,
		},
		{
			name:    "schema invalid",
			resp:    scriptedResponse{raw: json.RawMessage(`{"not":"valid review output"}`)},
			message: "reviewer output failed schema validation",
			wantRaw: `{"not":"valid review output"}`,
		},
		{
			name:    "duplicate key",
			resp:    scriptedResponse{raw: json.RawMessage(duplicateSummary)},
			message: "reviewer output failed schema validation",
			wantRaw: duplicateSummary,
			cause:   `duplicate key "summary"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			r := newScriptedReviewer(test.resp, scriptedResponse{raw: validReviewOutput("ready_to_build")})
			h := newHarness(t, r, baseConfig)
			open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			design := writeArtifactFile(t, "content")

			_, err = h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: design}})
			assertBrokerCode(t, err, CodeReviewerFailed)

			roundDir := store.RoundDir(open.SessionDir, 1)
			state, err := store.ReadState(roundDir)
			if err != nil {
				t.Fatalf("the failed round's state must remain: %v", err)
			}
			if state.State != store.RoundFailed || state.Error == nil || state.Error.Code != CodeReviewerFailed || state.Error.Message != test.message {
				t.Fatalf("failed state = %+v", state)
			}
			if _, ok := state.Error.Details["raw"]; ok {
				t.Fatal("raw output must never be stored in the error")
			}
			if test.cause != "" {
				cause, _ := state.Error.Details["cause"].(string)
				if !strings.Contains(cause, test.cause) {
					t.Fatalf("cause = '%s', want '%s'", cause, test.cause)
				}
			}
			for _, name := range []string{store.RoundLogFileName, store.ReviewFileName} {
				if _, err := os.Stat(filepath.Join(roundDir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed review published '%s', stat error = %v", name, err)
				}
			}
			rawPath := filepath.Join(roundDir, store.RawFileName)
			if test.wantRaw != "" {
				if got := readFile(t, rawPath); got != test.wantRaw {
					t.Fatalf("_raw.txt = %q, want %q", got, test.wantRaw)
				}
			} else if _, err := os.Stat(rawPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("no output was carried, so no _raw.txt is expected; stat err=%v", err)
			}

			round, err := h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: design}})
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if round.RoundNumber != 2 {
				t.Fatalf("retry round number = %d, want 2", round.RoundNumber)
			}

			// the failure is answered from disk by a broker that never saw it.
			_, err = h.broker().CollectRound(ctx, CollectRoundRequest{SessionID: open.SessionID, RoundNumber: 1})
			assertBrokerCode(t, err, CodeReviewerFailed)
			var be *Error
			errors.As(err, &be)
			if be.Message != test.message || be.Details["round_number"] != 1 || be.Details["state_path"] != filepath.Join(roundDir, store.StateFileName) {
				t.Fatalf("collected failure = %+v", be)
			}
			if test.wantRaw != "" && be.Details["raw_path"] != rawPath {
				t.Fatalf("collected failure raw_path = %v, want %q", be.Details["raw_path"], rawPath)
			}

			status, err := h.broker().SessionStatus(ctx, open.SessionID)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if status.RoundCount != 2 || status.Rounds[0].State != store.RoundFailed || status.Rounds[0].Error == nil || status.Rounds[1].State != store.RoundCompleted {
				t.Fatalf("status = %+v", status)
			}
		})
	}
}

func TestMaxFindingsFailsRoundAndKeepsRaw(t *testing.T) {
	ctx := context.Background()
	raw := reviewOutputWithRefs()
	h := newHarness(t, newScriptedReviewer(scriptedResponse{raw: raw}), baseConfig+"max_findings: 1\n")
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}})
	assertBrokerCode(t, err, CodeReviewerFailed)
	var be *Error
	errors.As(err, &be)
	if be.Message != "reviewer output exceeded max findings" || be.Details["findings"] != json.Number("2") {
		t.Fatalf("failure = %+v", be)
	}
	if got := readFile(t, filepath.Join(store.RoundDir(open.SessionDir, 1), store.RawFileName)); got != string(raw) {
		t.Fatalf("_raw.txt = %q", got)
	}
}

// a failure carrying a megabyte of output, with an error message just as long,
// leaves a small _state.json and every byte of the output in _raw.txt.
func TestLargeFailureOutputStaysOutOfState(t *testing.T) {
	ctx := context.Background()
	output := make([]byte, 1<<20)
	for i := range output {
		output[i] = byte('a' + i%26)
	}
	hugeMessage := strings.Repeat("m", 1<<20)
	r := newScriptedReviewer(scriptedResponse{err: &reviewer.OutputError{Err: errors.New(hugeMessage), Output: output}})
	h := newHarness(t, r, baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}})
	assertBrokerCode(t, err, CodeReviewerFailed)

	roundDir := store.RoundDir(open.SessionDir, 1)
	info, err := os.Stat(filepath.Join(roundDir, store.StateFileName))
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if info.Size() >= 16*1024 {
		t.Fatalf("_state.json is %d bytes, want under 16 KiB", info.Size())
	}
	raw, err := os.ReadFile(filepath.Join(roundDir, store.RawFileName))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if string(raw) != string(output) {
		t.Fatalf("_raw.txt holds %d bytes, want all %d of the output", len(raw), len(output))
	}
	state, err := store.ReadState(roundDir)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	cause, _ := state.Error.Details["cause"].(string)
	if !strings.Contains(cause, "[cut:") {
		t.Fatalf("the cut cause must say it was cut; got %d bytes", len(cause))
	}
}

// the launch guard is project-wide: a second launch is refused while a round
// runs, from another session and from another broker on the same directory.
func TestLaunchRefusedWhileRoundRuns(t *testing.T) {
	ctx := context.Background()
	r := newBlockingReviewer(validReviewOutput("ready_to_build"))
	h := newHarness(t, r, baseConfig)
	t.Cleanup(r.release)

	sessionA, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	if _, err := h.broker().StartReviewRound(ctx, StartRoundRequest{
		SessionID: sessionA.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "a")}},
	}); err != nil {
		t.Fatalf("start A: %v", err)
	}
	waitStarted(t, r)

	sessionB, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	_, err = h.broker().StartReviewRound(ctx, StartRoundRequest{
		SessionID: sessionB.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "b")}},
	})
	assertBrokerCode(t, err, CodeConflict)
	var be *Error
	errors.As(err, &be)
	if be.Details["running_session_id"] != sessionA.SessionID || be.Details["running_round_number"] != 1 {
		t.Fatalf("conflict must name the running round, got %+v", be)
	}
	if be.Details["started_by_this_server"] != true {
		t.Fatalf("a round started by this process must say so: %+v", be.Details)
	}
	for _, key := range []string{"started_at", "running_for", "monitor_command", "next_action"} {
		if _, ok := be.Details[key]; !ok {
			t.Fatalf("conflict details missing %q: %+v", key, be.Details)
		}
	}
	if !strings.Contains(be.Details["monitor_command"].(string), sessionA.SessionID) ||
		!strings.Contains(be.Details["next_action"].(string), "collect_round") {
		t.Fatalf("conflict must point at the running session: %+v", be.Details)
	}
	assertNoRounds(t, sessionB.SessionDir)

	_, err = h.broker().StartReviewRound(ctx, StartRoundRequest{
		SessionID: sessionA.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "a")}},
	})
	assertBrokerCode(t, err, CodeConflict)
	_, err = h.broker().CollectRound(ctx, CollectRoundRequest{SessionID: sessionA.SessionID})
	assertBrokerCode(t, err, CodeConflict)
	_, err = h.broker().CloseSession(ctx, CloseSessionRequest{SessionID: sessionA.SessionID})
	assertBrokerCode(t, err, CodeConflict)
	status, err := h.broker().SessionStatus(ctx, sessionA.SessionID)
	if err != nil || status.Rounds[0].State != store.RoundRunning {
		t.Fatalf("a round with a live runner must stay running: status=%+v err=%v", status, err)
	}

	r.release()
	h.waitDone(store.RoundDir(sessionA.SessionDir, 1))
	if _, err := h.broker().CollectRound(ctx, CollectRoundRequest{SessionID: sessionA.SessionID, RoundNumber: 1}); err != nil {
		t.Fatalf("collect after release: %v", err)
	}
	assertLockFree(t, h.logDest)
	if _, err := h.startAndCollect(sessionB.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "b")}}); err != nil {
		t.Fatalf("launch after the running round finished: %v", err)
	}
}

// the launch conflict says whether the running round was started by the
// answering server, by comparing the lock note's server pid with its own.
func TestLaunchConflictNamesAnotherServer(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	lock, err := runlock.Take(h.logDest)
	if err != nil {
		t.Fatalf("take lock: %v", err)
	}
	defer lock.Release()
	if err := lock.WriteNote(runlock.Note{SessionID: open.SessionID, RoundNumber: 1, ServerPID: os.Getpid() + 1, StartedAt: time.Now()}); err != nil {
		t.Fatalf("write note: %v", err)
	}
	_, err = h.broker().StartReviewRound(ctx, StartRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "x")}},
	})
	assertBrokerCode(t, err, CodeConflict)
	var be *Error
	errors.As(err, &be)
	if be.Details["started_by_this_server"] != false {
		t.Fatalf("a round started by another server must say so: %+v", be.Details)
	}
}

// a round whose state says running while the lock is free has no runner. each
// reader that meets it records it failed with reason runner_lost.
func TestDeadRoundMarkedRunnerLost(t *testing.T) {
	ops := []struct {
		name  string
		run   func(b *Broker, sessionID string) error
		check func(t *testing.T, err error, sessionDir string)
	}{
		{
			name: "collect_round",
			run: func(b *Broker, sessionID string) error {
				_, err := b.CollectRound(context.Background(), CollectRoundRequest{SessionID: sessionID, RoundNumber: 1})
				return err
			},
			check: func(t *testing.T, err error, _ string) {
				assertBrokerCode(t, err, CodeReviewerFailed)
				var be *Error
				errors.As(err, &be)
				if be.Details["reason"] != store.ReasonRunnerLost {
					t.Fatalf("collected failure = %+v", be)
				}
			},
		},
		{
			name: "session_status",
			run: func(b *Broker, sessionID string) error {
				_, err := b.SessionStatus(context.Background(), sessionID)
				return err
			},
			check: func(t *testing.T, err error, _ string) {
				if err != nil {
					t.Fatalf("status: %v", err)
				}
			},
		},
		{
			name: "close_session",
			run: func(b *Broker, sessionID string) error {
				_, err := b.CloseSession(context.Background(), CloseSessionRequest{SessionID: sessionID})
				return err
			},
			check: func(t *testing.T, err error, sessionDir string) {
				if err != nil {
					t.Fatalf("close must succeed once the dead round is recorded: %v", err)
				}
				synopsis := readFile(t, filepath.Join(sessionDir, store.SynopsisFileName))
				if !strings.Contains(synopsis, "| 01 | ") || !strings.Contains(synopsis, "- State: failed") {
					t.Fatalf("synopsis must list the lost round:\n%s", synopsis)
				}
			},
		},
		{
			name: "start_review_round",
			run: func(b *Broker, sessionID string) error {
				_, err := b.StartReviewRound(context.Background(), StartRoundRequest{
					SessionID: sessionID,
					Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
				})
				return err
			},
			check: func(t *testing.T, err error, sessionDir string) {
				if err != nil {
					t.Fatalf("start must go through once the dead round is recorded: %v", err)
				}
				if !store.RoundExists(store.RoundDir(sessionDir, 2)) {
					t.Fatal("the next round must be round 2")
				}
			},
		},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			h := newHarness(t, newScriptedReviewer(), baseConfig)
			open, err := h.broker().OpenSession(context.Background(), OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			roundDir := writeRunningRound(t, h.logDest, open.SessionID, open.SessionDir, 1)

			err = op.run(h.broker(), open.SessionID)
			if op.name == "start_review_round" {
				h.waitDone(store.RoundDir(open.SessionDir, 2))
			}
			op.check(t, err, open.SessionDir)

			state, err := store.ReadState(roundDir)
			if err != nil {
				t.Fatalf("read state: %v", err)
			}
			if state.State != store.RoundFailed || state.Error == nil || state.Error.Code != CodeReviewerFailed || state.Error.Details["reason"] != store.ReasonRunnerLost {
				t.Fatalf("dead round state = %+v", state)
			}
		})
	}
}

// the dead-round check must decide from a read taken while it holds the lock.
// here the runner finishes, writing completed, between the check's first read
// and its hold on the lock. a check that decided from its first read would
// overwrite the completed round with runner_lost.
func TestDeadRoundCheckRereadsUnderLock(t *testing.T) {
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	open, err := h.broker().OpenSession(context.Background(), OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	roundDir := writeRunningRound(t, h.logDest, open.SessionID, open.SessionDir, 1)

	b := h.broker()
	took := false
	b.takeLock = func(logDestination string) (*runlock.Lock, error) {
		lock, err := runlock.TakeOnce(logDestination)
		if err != nil {
			return nil, err
		}
		took = true
		state, err := store.ReadState(roundDir)
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		now := time.Now().UTC()
		state.State = store.RoundCompleted
		state.CompletedAt = &now
		if err := store.WriteState(roundDir, state); err != nil {
			t.Fatalf("write completed: %v", err)
		}
		return lock, nil
	}

	status, err := b.SessionStatus(context.Background(), open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !took {
		t.Fatal("the check never took the lock")
	}
	state, err := store.ReadState(roundDir)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.State != store.RoundCompleted || state.Error != nil {
		t.Fatalf("the check overwrote a round that finished under it: %+v", state)
	}
	if status.Rounds[0].State != store.RoundCompleted {
		t.Fatalf("status must report the state read under the lock: %+v", status.Rounds[0])
	}
	assertLockFree(t, h.logDest)
}

func TestRecordNotesOnFailedRoundIsConflict(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(scriptedResponse{err: errors.New("boom")}), baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}})
	assertBrokerCode(t, err, CodeReviewerFailed)

	_, err = h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "trying to note a failure",
	})
	assertBrokerCode(t, err, CodeConflict)
	roundDir := store.RoundDir(open.SessionDir, 1)
	for _, name := range []string{store.NotesFileName, store.NotesJSONFileName} {
		if _, err := os.Stat(filepath.Join(roundDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must not be written for a failed round; stat err=%v", name, err)
		}
	}
}

// a round directory with no _state.json is a leftover from a server that died
// while preparing it. the next start removes it and reuses the number.
func TestLeftoverRoundDirRemovedAndNumberReused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	leftover := store.RoundDir(open.SessionDir, 1)
	if err := os.Mkdir(leftover, 0o700); err != nil {
		t.Fatalf("mkdir leftover: %v", err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "stale-snapshot"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	status, err := h.broker().SessionStatus(ctx, open.SessionID)
	if err != nil || status.RoundCount != 0 {
		t.Fatalf("a leftover directory is not a round: status=%+v err=%v", status, err)
	}

	round, err := h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}})
	if err != nil {
		t.Fatalf("round: %v", err)
	}
	if round.RoundNumber != 1 {
		t.Fatalf("round number = %d, want the leftover's number 1", round.RoundNumber)
	}
	if _, err := os.Stat(filepath.Join(leftover, "stale-snapshot")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the leftover's contents must be gone; stat err=%v", err)
	}
}

// session ids are checked before they are joined into a path. a directory
// outside the log destination holds a valid session.json, so an operation that
// read it would succeed instead of answering not_found.
func TestSessionIDsAreCheckedBeforeUse(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	if _, err := h.broker().OpenSession(ctx, OpenSessionRequest{}); err != nil {
		t.Fatalf("open: %v", err)
	}
	outside := filepath.Join(filepath.Dir(h.logDest), "x")
	if err := os.MkdirAll(store.RoundDir(outside, 1), 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := store.WriteSession(outside, store.Session{SessionID: "../x", OpenedAt: time.Now(), State: store.SessionActive}); err != nil {
		t.Fatalf("write outside session: %v", err)
	}
	if err := store.WriteState(store.RoundDir(outside, 1), store.State{State: store.RoundCompleted}); err != nil {
		t.Fatalf("write outside state: %v", err)
	}
	notSession := store.SessionDir(h.logDest, "s_oldLayout012")
	if err := os.Mkdir(notSession, 0o700); err != nil {
		t.Fatalf("mkdir old layout: %v", err)
	}

	artifact := []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}}
	for _, id := range []string{"../x", "", "s_aaaaaaaaaaaa", "s_oldLayout012"} {
		t.Run(strconv.Quote(id), func(t *testing.T) {
			b := h.broker()
			_, err := b.SessionStatus(ctx, id)
			assertBrokerCode(t, err, CodeNotFound)
			_, err = b.CollectRound(ctx, CollectRoundRequest{SessionID: id, RoundNumber: 1})
			assertBrokerCode(t, err, CodeNotFound)
			_, err = b.RecordRoundNotes(ctx, RecordRoundNotesRequest{SessionID: id, RoundNumber: 1, Commentary: "x"})
			assertBrokerCode(t, err, CodeNotFound)
			_, err = b.CloseSession(ctx, CloseSessionRequest{SessionID: id})
			assertBrokerCode(t, err, CodeNotFound)
			_, err = b.StartReviewRound(ctx, StartRoundRequest{SessionID: id, Artifacts: artifact})
			assertBrokerCode(t, err, CodeNotFound)
		})
	}
	if _, err := os.Stat(filepath.Join(outside, store.SynopsisFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("close_session wrote outside the log destination")
	}
	if dirs, _ := store.RoundDirs(outside); len(dirs) != 1 {
		t.Fatalf("start_review_round wrote outside the log destination: %v", dirs)
	}

	_, err := h.broker().SessionStatus(ctx, "s_oldLayout012")
	var be *Error
	errors.As(err, &be)
	if reason, _ := be.Details["reason"].(string); !strings.Contains(reason, "not a session in this layout") {
		t.Fatalf("a directory without session.json must say why: %+v", be.Details)
	}
}

func TestCorruptMachineFilesAreInternalErrors(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}}); err != nil {
		t.Fatalf("round: %v", err)
	}
	reviewPath := filepath.Join(store.RoundDir(open.SessionDir, 1), store.ReviewFileName)
	withUnknownKey := strings.Replace(readFile(t, reviewPath), "{", `{"unexpected":true,`, 1)
	if err := os.WriteFile(reviewPath, []byte(withUnknownKey), 0o600); err != nil {
		t.Fatalf("write unknown key into review: %v", err)
	}
	_, err = h.broker().CollectRound(ctx, CollectRoundRequest{SessionID: open.SessionID, RoundNumber: 1})
	assertBrokerCode(t, err, CodeInternalError)
	if details := ErrorInfoFrom(err).Details; details["path"] != reviewPath {
		t.Fatalf("expected error to name '%s', got %+v", reviewPath, details)
	}

	statePath := filepath.Join(store.RoundDir(open.SessionDir, 1), store.StateFileName)
	if err := os.WriteFile(statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt state: %v", err)
	}
	_, err = h.broker().CollectRound(ctx, CollectRoundRequest{SessionID: open.SessionID, RoundNumber: 1})
	assertBrokerCode(t, err, CodeInternalError)
	if !strings.Contains(err.Error(), statePath) {
		t.Fatalf("expected the error to name '%s', got %v", statePath, err)
	}

	sessionPath := filepath.Join(open.SessionDir, store.SessionFileName)
	if err := os.WriteFile(sessionPath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt session: %v", err)
	}
	_, err = h.broker().SessionStatus(ctx, open.SessionID)
	assertBrokerCode(t, err, CodeInternalError)
	if !strings.Contains(err.Error(), sessionPath) {
		t.Fatalf("expected the error to name '%s', got %v", sessionPath, err)
	}
}

// the round timeout bounds a reviewer. a reviewer that blocks past it and fails
// fails the round with reason timeout; one that blocks past it and then returns a
// valid review completes the round, because a finished review is not thrown
// away for its process's failure to exit.
func TestRoundTimeout(t *testing.T) {
	ctx := context.Background()
	timeoutUser := baseUser + "  timeout: 50ms\n"

	t.Run("error after the deadline fails with reason timeout", func(t *testing.T) {
		h := newHarness(t, deadlineReviewer{}, baseConfig)
		h.writeUserConfig(timeoutUser)
		open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		_, err = h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}})
		assertBrokerCode(t, err, CodeReviewerFailed)
		var be *Error
		errors.As(err, &be)
		if be.Details["reason"] != store.ReasonTimeout {
			t.Fatalf("failure = %+v, want reason timeout", be)
		}
		assertLockFree(t, h.logDest)
	})

	t.Run("valid review after the deadline completes", func(t *testing.T) {
		h := newHarness(t, deadlineReviewer{raw: validReviewOutput("ready_to_build")}, baseConfig)
		h.writeUserConfig(timeoutUser)
		open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		round, err := h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}})
		if err != nil {
			t.Fatalf("a valid review returned after the deadline must complete the round: %v", err)
		}
		if round.RoundNumber != 1 || len(round.Reviewers) != 1 {
			t.Fatalf("round = %+v", round)
		}
	})
}

func TestRecordRoundNotesValidationAndReplacement(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()}), baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}}); err != nil {
		t.Fatalf("round: %v", err)
	}

	_, err = h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{SessionID: open.SessionID, RoundNumber: 1})
	assertBrokerCode(t, err, CodeUserError)
	_, err = h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Decisions:   []Decision{{Ref: "missing", Disposition: "fixed", Note: "nope"}},
	})
	assertBrokerCode(t, err, CodeUserError)
	_, err = h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{SessionID: open.SessionID, RoundNumber: 9, Commentary: "x"})
	assertBrokerCode(t, err, CodeNotFound)

	if _, err := h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "first",
		Decisions:   []Decision{{Ref: "Q-1", Disposition: "deferred", Note: "later"}},
	}); err != nil {
		t.Fatalf("record first notes: %v", err)
	}
	roundDir := store.RoundDir(open.SessionDir, 1)
	notesPath := filepath.Join(roundDir, store.NotesFileName)
	content := readFile(t, notesPath)
	if !strings.Contains(content, "first") || !strings.Contains(content, "deferred") {
		t.Fatalf("expected first notes:\n%s", content)
	}
	notes, err := store.ReadNotes(roundDir)
	if err != nil || notes.Commentary != "first" || len(notes.Decisions) != 1 || notes.Decisions[0].Ref != "Q-1" {
		t.Fatalf("_notes.json = %+v err=%v", notes, err)
	}

	if _, err := h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{
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
	if notes, err := store.ReadNotes(roundDir); err != nil || notes.Commentary != "second" || len(notes.Decisions) != 0 {
		t.Fatalf("_notes.json after replace = %+v err=%v", notes, err)
	}
}

func TestRecordRoundNotesUnknownRefListsValidRefs(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()}), baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}}); err != nil {
		t.Fatalf("round: %v", err)
	}
	_, err = h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Decisions:   []Decision{{Ref: "bogus", Disposition: "fixed", Note: "nope"}},
	})
	assertBrokerCode(t, err, CodeUserError)
	var be *Error
	errors.As(err, &be)
	got, ok := be.Details["valid_refs"].([]string)
	if !ok {
		t.Fatalf("expected valid_refs in details, got %+v", be.Details)
	}
	if want := []string{"C-1", "Q-1"}; !slices.Equal(got, want) {
		t.Fatalf("valid_refs = %v, want %v (sorted)", got, want)
	}
}

func TestRecordRoundNotesRejectsLegacyAcceptedDisposition(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()}), baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := h.startAndCollect(open.SessionID, []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}}); err != nil {
		t.Fatalf("round: %v", err)
	}
	_, err = h.broker().RecordRoundNotes(ctx, RecordRoundNotesRequest{
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
			h := newHarness(t, newScriptedReviewer(), baseConfig)
			open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, err = h.broker().StartReviewRound(ctx, StartRoundRequest{SessionID: open.SessionID, Artifacts: test.artifacts})
			assertBrokerCode(t, err, CodeUserError)
			assertNoRounds(t, open.SessionDir)
		})
	}
}

func TestOpenSessionRejectsBadLogDestination(t *testing.T) {
	h := newHarness(t, newScriptedReviewer(), "log_destination: missing-parent/reviews\n")
	_, err := h.broker().OpenSession(context.Background(), OpenSessionRequest{})
	assertBrokerCode(t, err, CodeUserError)
}

func TestCloseSessionWritesSynopsisWithoutRounds(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, newScriptedReviewer(), baseConfig)
	open, err := h.broker().OpenSession(ctx, OpenSessionRequest{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	closed, err := h.broker().CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	synopsis := readFile(t, closed.SynopsisPath)
	for _, want := range []string{"round_count: 0", "# Session synopsis", "## Summary", "Session closed with no rounds."} {
		if !strings.Contains(synopsis, want) {
			t.Errorf("empty-rounds synopsis missing %q\n---\n%s", want, synopsis)
		}
	}
	_, err = h.broker().CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeConflict)
}

// harness is one project directory: a mercurius.yaml, its log destination, and
// an in-process launcher whose reviewer is a Go value. broker() returns a new
// Broker each call.
type harness struct {
	t          *testing.T
	dir        string
	configPath string
	userPath   string
	logDest    string
	launcher   *runner.InProcess
}

func newHarness(t *testing.T, r reviewer.Reviewer, config string) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{
		t:          t,
		dir:        dir,
		configPath: filepath.Join(dir, "mercurius.yaml"),
		userPath:   filepath.Join(dir, "user", "mercurius", "config.yaml"),
		logDest:    filepath.Join(dir, "reviews"),
		launcher: runner.NewInProcess(func(store.RunReviewer) (reviewer.Reviewer, error) {
			return r, nil
		}),
	}
	h.writeConfig(config)
	h.writeUserConfig(baseUser)
	t.Cleanup(h.launcher.Wait)
	return h
}

func (h *harness) writeConfig(body string) {
	h.t.Helper()
	if err := os.WriteFile(h.configPath, []byte(body), 0o600); err != nil {
		h.t.Fatalf("write config: %v", err)
	}
}

// writeUserConfig writes the harness's user-level file. no test reads the
// developer's real user-level config.
func (h *harness) writeUserConfig(body string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.userPath), 0o700); err != nil {
		h.t.Fatalf("mkdir user config: %v", err)
	}
	if err := os.WriteFile(h.userPath, []byte(body), 0o600); err != nil {
		h.t.Fatalf("write user config: %v", err)
	}
}

func (h *harness) broker() *Broker {
	return New(Options{
		ConfigPath:     h.configPath,
		UserConfigPath: h.userPath,
		Launcher:       h.launcher,
	})
}

// waitDone polls a round's _state.json until it leaves running.
func (h *harness) waitDone(roundDir string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := store.ReadState(roundDir)
		if err == nil && state.State != store.RoundRunning {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("round '%s' did not finish; last state=%+v err=%v", roundDir, state, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startAndCollect starts a round with one broker, waits on disk, and collects it
// with another.
func (h *harness) startAndCollect(sessionID string, artifacts []Artifact) (CollectedRoundResponse, error) {
	h.t.Helper()
	started, err := h.broker().StartReviewRound(context.Background(), StartRoundRequest{SessionID: sessionID, Artifacts: artifacts})
	if err != nil {
		return CollectedRoundResponse{}, err
	}
	h.waitDone(filepath.Dir(started.StatusPath))
	return h.broker().CollectRound(context.Background(), CollectRoundRequest{SessionID: sessionID, RoundNumber: started.RoundNumber})
}

// writeRunningRound hand-writes a round whose state says running, and a lock
// note naming it, with the lock itself left free: a round whose runner is gone.
func writeRunningRound(t *testing.T, logDest string, sessionID string, sessionDir string, n int) string {
	t.Helper()
	roundDir := store.RoundDir(sessionDir, n)
	if err := os.Mkdir(roundDir, 0o700); err != nil {
		t.Fatalf("mkdir round: %v", err)
	}
	now := time.Now().UTC()
	if err := store.WriteJSON(filepath.Join(roundDir, store.RunFileName), store.Run{
		SessionID:   sessionID,
		RoundNumber: n,
		OpenedAt:    now,
		ServerPID:   os.Getpid(),
		Reviewer:    store.RunReviewer{Name: "dummy", Impl: "dummy", Timeout: time.Minute},
		MaxFindings: 6,
	}); err != nil {
		t.Fatalf("write run: %v", err)
	}
	if err := store.WriteState(roundDir, store.State{State: store.RoundRunning, StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	lock, err := runlock.Take(logDest)
	if err != nil {
		t.Fatalf("take lock: %v", err)
	}
	defer lock.Release()
	if err := lock.WriteNote(runlock.Note{SessionID: sessionID, RoundNumber: n, ServerPID: os.Getpid(), StartedAt: now}); err != nil {
		t.Fatalf("write note: %v", err)
	}
	return roundDir
}

func assertLockFree(t *testing.T, logDest string) {
	t.Helper()
	lock, err := runlock.TakeOnce(logDest)
	if err != nil {
		t.Fatalf("expected the runner lock to be free: %v", err)
	}
	lock.Release()
}

func assertNoRounds(t *testing.T, sessionDir string) {
	t.Helper()
	dirs, err := store.RoundDirs(sessionDir)
	if err != nil {
		t.Fatalf("list rounds: %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("expected no round directories, got %v", dirs)
	}
}

func readSessionFile(t *testing.T, sessionDir string) store.Session {
	t.Helper()
	session, err := store.ReadSession(sessionDir)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	return session
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
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

	r.captured = append(r.captured, req)
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
	return append([]reviewer.ReviewRequest(nil), r.captured...)
}

type blockingReviewer struct {
	started     chan struct{}
	releaseC    chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	raw         json.RawMessage
}

func newBlockingReviewer(raw json.RawMessage) *blockingReviewer {
	return &blockingReviewer{
		started:  make(chan struct{}),
		releaseC: make(chan struct{}),
		raw:      append(json.RawMessage(nil), raw...),
	}
}

func (r *blockingReviewer) Review(ctx context.Context, _ reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	r.startOnce.Do(func() { close(r.started) })
	select {
	case <-r.releaseC:
		return reviewer.ReviewResponse{Raw: append(json.RawMessage(nil), r.raw...), UsageNotes: "blocking"}, nil
	case <-ctx.Done():
		return reviewer.ReviewResponse{}, ctx.Err()
	}
}

func (r *blockingReviewer) release() {
	r.releaseOnce.Do(func() { close(r.releaseC) })
}

func waitStarted(t *testing.T, r *blockingReviewer) {
	t.Helper()
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("reviewer did not start")
	}
}

// deadlineReviewer blocks until its context's deadline passes, then returns its
// raw review, or the context's error when it has none.
type deadlineReviewer struct {
	raw json.RawMessage
}

func (r deadlineReviewer) Review(ctx context.Context, _ reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	<-ctx.Done()
	if len(r.raw) == 0 {
		return reviewer.ReviewResponse{}, ctx.Err()
	}
	return reviewer.ReviewResponse{Raw: append(json.RawMessage(nil), r.raw...), UsageNotes: "late"}, nil
}

func assertSnapshot(t *testing.T, manifest ArtifactManifestEntry, want string) {
	t.Helper()
	if got := readFile(t, manifest.SnapshotPath); got != want {
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
	if frontmatter["prompt_path"] != store.PromptFileName {
		t.Fatalf("frontmatter prompt_path = %q", frontmatter["prompt_path"])
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
	raw, err := json.Marshal(map[string]any{
		"verdict":        verdict,
		"summary":        "reviewed",
		"concerns":       []any{},
		"questions":      []any{},
		"advisory_notes": []any{},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func reviewOutputWithRefs() json.RawMessage {
	return json.RawMessage(`{
		"verdict": "needs_changes",
		"summary": "the design needs a detail and a budget decision",
		"concerns": [{
			"id": "C-1",
			"severity": "major",
			"location": "work-order:M3",
			"claim": "missing detail",
			"rationale": "implementation would diverge",
			"suggestion": "clarify the work order"
		}],
		"questions": [{
			"id": "Q-1",
			"topic": "budget",
			"why_it_blocks": "cannot judge loop length"
		}],
		"advisory_notes": []
	}`)
}

func writeArtifactFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

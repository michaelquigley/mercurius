package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/df/dd"
	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/runner"
	"github.com/michaelquigley/mercurius/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolDiscovery(t *testing.T) {
	ctx, client := newTestClient(t)

	result, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	got := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		got = append(got, tool.Name)
	}
	slices.Sort(got)

	want := []string{
		"close_session",
		"collect_round",
		"open_session",
		"record_round_notes",
		"session_status",
		"start_review_round",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestDummyReviewFlow(t *testing.T) {
	ctx, client := newTestClient(t)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "open_session",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	openOutput := bindStructured[OpenSessionOutput](t, openResult)
	if openOutput.SessionID == "" {
		t.Fatal("expected session id")
	}
	if openOutput.MaxFindings != config.DefaultMaxFindings {
		t.Fatalf("max findings = %d, want %d", openOutput.MaxFindings, config.DefaultMaxFindings)
	}
	if openOutput.Reviewer == nil || openOutput.Reviewer.Name != "dummy" || openOutput.Reviewer.Timeout != "20m0s" {
		t.Fatalf("open reviewer = %+v", openOutput.Reviewer)
	}

	round1 := startAndCollectRoundTool(t, ctx, client, openOutput.SessionID, artifactPath)
	if round1.RoundNumber != 1 {
		t.Fatalf("round number = %d, want 1", round1.RoundNumber)
	}
	if len(round1.Reviewers) != 1 || round1.Reviewers[0].ReviewerName != "dummy" {
		t.Fatalf("reviewers = %+v", round1.Reviewers)
	}
	if len(round1.Manifest) != 1 || round1.Manifest[0].Name != "design.md" {
		t.Fatalf("manifest = %+v", round1.Manifest)
	}
	if !strings.Contains(round1.NextAction, "pause") {
		t.Fatalf("round next action = %q", round1.NextAction)
	}
	if round1.Triage.TotalFindings != 0 || round1.Triage.RemainingFindings != 0 || len(round1.Triage.Findings) != 0 || round1.Triage.NextFinding != nil {
		t.Fatalf("no-finding triage = %+v", round1.Triage)
	}

	notesResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "record_round_notes",
		Arguments: map[string]any{
			"session_id":   openOutput.SessionID,
			"round_number": round1.RoundNumber,
			"commentary":   "the dummy reviewer was accepted.",
		},
	})
	if err != nil {
		t.Fatalf("record notes: %v", err)
	}
	notesOutput := bindStructured[RecordRoundNotesOutput](t, notesResult)
	if !notesOutput.CommentaryRecorded || notesOutput.DecisionsRecorded != 0 {
		t.Fatalf("notes output = %+v", notesOutput)
	}

	statusResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "session_status",
		Arguments: map[string]any{
			"session_id": openOutput.SessionID,
		},
	})
	if err != nil {
		t.Fatalf("session status: %v", err)
	}
	statusOutput := bindStructured[SessionStatusOutput](t, statusResult)
	if statusOutput.RoundCount != 1 || len(statusOutput.Rounds) != 1 || !statusOutput.Rounds[0].HasNotes {
		t.Fatalf("status output = %+v", statusOutput)
	}
	round := statusOutput.Rounds[0]
	if round.State != "completed" || round.Reviewer.Name != "dummy" || round.MaxFindings != config.DefaultMaxFindings || round.Error != nil || round.CompletedAt == nil {
		t.Fatalf("round status = %+v", round)
	}
	if filepath.Base(round.StatePath) != "_state.json" || filepath.Base(round.LogPath) != "_round.md" {
		t.Fatalf("round paths = %+v", round)
	}

	closeResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "close_session",
		Arguments: map[string]any{
			"session_id": openOutput.SessionID,
		},
	})
	if err != nil {
		t.Fatalf("close session: %v", err)
	}
	closeOutput := bindStructured[CloseSessionOutput](t, closeResult)
	if closeOutput.SessionID != openOutput.SessionID || closeOutput.ClosedAt.IsZero() {
		t.Fatalf("close output = %+v", closeOutput)
	}
	if closeOutput.SynopsisPath == "" {
		t.Fatal("close output did not return a synopsis_path")
	}
	if _, err := os.Stat(closeOutput.SynopsisPath); err != nil {
		t.Fatalf("expected synopsis file at %s: %v", closeOutput.SynopsisPath, err)
	}
}

func TestBrokerErrorsBecomeToolErrors(t *testing.T) {
	ctx, client := newTestClient(t)

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "session_status",
		Arguments: map[string]any{
			"session_id": "s_missing",
		},
	})
	assertToolError(t, result, err, broker.CodeNotFound)
}

func TestArtifactNameValidationOnStartRound(t *testing.T) {
	ctx, client := newTestClient(t)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "open_session",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sessionID := bindStructured[OpenSessionOutput](t, openResult).SessionID

	tests := []string{
		"dir/file",
		".",
		"..",
		"bad name",
		"",
		strings.Repeat("a", 65),
	}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := client.CallTool(ctx, &mcp.CallToolParams{
				Name: "start_review_round",
				Arguments: map[string]any{
					"session_id": sessionID,
					"artifacts": []map[string]any{{
						"name": name,
						"path": artifactPath,
					}},
				},
			})
			assertToolError(t, result, err, broker.CodeUserError)
		})
	}
}

func TestFailedRoundReturnsToolErrorAndStatusShowsIt(t *testing.T) {
	ctx, client, _ := newTestClientWith(t, failingReviewer{}, baseTestConfig)
	sessionID, artifactPath := openTestSession(t, ctx, client)

	payload := startAndCollectRoundToolError(t, ctx, client, sessionID, artifactPath, broker.CodeReviewerFailed)
	if payload.Details["round_number"] != float64(1) || payload.Details["state_path"] == nil {
		t.Fatalf("failure details = %+v", payload.Details)
	}

	statusResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "session_status",
		Arguments: map[string]any{"session_id": sessionID},
	})
	if err != nil {
		t.Fatalf("session status: %v", err)
	}
	status := bindStructured[SessionStatusOutput](t, statusResult)
	if status.RoundCount != 1 || status.Rounds[0].State != "failed" {
		t.Fatalf("a failed round stays listed: %+v", status)
	}
	if status.Rounds[0].Error == nil || status.Rounds[0].Error.Code != broker.CodeReviewerFailed {
		t.Fatalf("round error = %+v", status.Rounds[0].Error)
	}

	// the retry is the next round.
	if started := startRoundTool(t, ctx, client, sessionID, artifactPath); started.RoundNumber != 2 {
		t.Fatalf("retry round number = %d, want 2", started.RoundNumber)
	}
}

// the server reads no configuration at startup. With no mercurius.yaml, a tool
// call answers user_error naming the path; once the file is written, the same
// client's next call succeeds with no reconnect.
func TestServerStartsWithoutConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "some-project")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(dir, "mercurius.yaml")
	server, _, err := New(Options{
		ConfigPath:     configPath,
		UserConfigPath: filepath.Join(dir, "user.yaml"),
		Launcher:       inProcessLauncher(t),
	})
	if err != nil {
		t.Fatalf("new server without a config: %v", err)
	}
	ctx, client := newTestClientForServer(t, server)
	if name := client.InitializeResult().ServerInfo.Name; name != "some-project" {
		t.Fatalf("server name = %q, want the project directory's name", name)
	}

	first, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	payload := assertToolError(t, first, err, broker.CodeUserError)
	if payload.Details["config_path"] != configPath {
		t.Fatalf("expected the error to name '%s', got %+v", configPath, payload)
	}
	if text := first.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, configPath) {
		t.Fatalf("expected the error text to name '%s', got %q", configPath, text)
	}

	writeConfigFile(t, configPath, baseTestConfig)
	writeConfigFile(t, filepath.Join(dir, "user.yaml"), baseTestUser)
	second, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil || second.IsError {
		t.Fatalf("open after writing the config: result=%+v err=%v", second, err)
	}
	opened := bindStructured[OpenSessionOutput](t, second)
	if opened.Reviewer == nil || opened.Reviewer.Source != "user config" {
		t.Fatalf("open must report the reviewer and where it came from: %+v", opened.Reviewer)
	}
	sessionID := opened.SessionID
	collected := startAndCollectRoundTool(t, ctx, client, sessionID, writeArtifact(t, "design.md", "# design\n"))
	if collected.RoundNumber != 1 || collected.Reviewers[0].ReviewerName != "dummy" {
		t.Fatalf("collected = %+v", collected)
	}
}

func TestAsyncReviewTools(t *testing.T) {
	reviewerImpl := newBlockingReviewer(validReviewOutput())
	ctx, client, _ := newTestClientWith(t, reviewerImpl, baseTestConfig)
	t.Cleanup(reviewerImpl.release)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "open_session",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sessionID := bindStructured[OpenSessionOutput](t, openResult).SessionID

	startResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "start_review_round",
		Arguments: map[string]any{
			"session_id": sessionID,
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
		},
	})
	if err != nil {
		t.Fatalf("start review round: %v", err)
	}
	start := bindStructured[StartReviewRoundOutput](t, startResult)
	if start.RoundNumber != 1 || start.State != "running" || start.MonitorCommand == "" || start.Reviewer.Name != "dummy" {
		t.Fatalf("start output = %+v", start)
	}
	if filepath.Base(start.StatusPath) != "_state.json" {
		t.Fatalf("status path = %q, want the round's _state.json", start.StatusPath)
	}
	waitBlockingStarted(t, reviewerImpl)

	collectResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "collect_round",
		Arguments: map[string]any{
			"session_id":   sessionID,
			"round_number": 1,
		},
	})
	assertToolError(t, collectResult, err, broker.CodeConflict)

	// a second launch, even from a new session, is refused and names the round.
	otherSession, _ := openTestSession(t, ctx, client)
	refused, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "start_review_round",
		Arguments: map[string]any{
			"session_id": otherSession,
			"artifacts":  []map[string]any{{"name": "design.md", "path": artifactPath}},
		},
	})
	conflict := assertToolError(t, refused, err, broker.CodeConflict)
	if conflict.Details["running_session_id"] != sessionID {
		t.Fatalf("conflict must name the running session: %+v", conflict)
	}

	reviewerImpl.release()
	var collected CollectedRoundOutput
	deadline := time.After(5 * time.Second)
	for {
		collectResult, err = client.CallTool(ctx, &mcp.CallToolParams{
			Name: "collect_round",
			Arguments: map[string]any{
				"session_id":   sessionID,
				"round_number": 1,
			},
		})
		if err == nil && collectResult != nil && !collectResult.IsError {
			collected = bindStructured[CollectedRoundOutput](t, collectResult)
			break
		}
		select {
		case <-deadline:
			t.Fatalf("collect did not complete; result=%+v err=%v", collectResult, err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if collected.RoundNumber != 1 || collected.LogPath == "" {
		t.Fatalf("collected = %+v", collected)
	}
}

func TestCollectRoundTriageConcernFirst(t *testing.T) {
	ctx, client := newTestClientWithFixedReviewer(t, reviewOutputWithConcernAndQuestion())
	sessionID, artifactPath := openTestSession(t, ctx, client)

	collected := startAndCollectRoundTool(t, ctx, client, sessionID, artifactPath)
	if collected.Triage.TotalFindings != 2 || collected.Triage.RemainingFindings != 1 {
		t.Fatalf("triage counts = %+v", collected.Triage)
	}
	if len(collected.Triage.Findings) != 2 {
		t.Fatalf("triage findings = %+v", collected.Triage.Findings)
	}
	finding := collected.Triage.NextFinding
	if finding == nil {
		t.Fatal("expected next finding")
	}
	if collected.Triage.Findings[0].Ref != finding.Ref || collected.Triage.Findings[1].Ref != "Q-1" || collected.Triage.Findings[1].Kind != "question" {
		t.Fatalf("triage finding order = %+v; next=%+v", collected.Triage.Findings, finding)
	}
	if finding.ReviewerName != "triage" || finding.Ref != "C-1" || finding.Kind != "concern" {
		t.Fatalf("next finding identity = %+v", finding)
	}
	for _, want := range []string{
		"present all entries in triage.findings",
		"walk findings one at a time",
		// radical compression is the stated default
		"plainest, fewest-words version",
		// the decision gate sits before the action
		"stop and wait for the user's decision before acting",
		"confidence that you can predict their decision is not consent",
		"Implement only after they respond",
	} {
		if !strings.Contains(collected.Triage.Guidance, want) {
			t.Fatalf("triage guidance missing %q:\n%s", want, collected.Triage.Guidance)
		}
	}
}

func TestTriageSkipsInvalidReview(t *testing.T) {
	valid, err := dd.DecodeStrictJSON(reviewOutputWithConcernAndQuestion())
	if err != nil {
		t.Fatalf("decode review fixture: %v", err)
	}
	invalid := maps.Clone(valid)
	invalid["unexpected"] = true
	triage := triageOutput([]broker.ReviewerResult{
		{ReviewerName: "invalid", Raw: invalid},
		{ReviewerName: "valid", Raw: valid},
	})
	if len(triage.Findings) != 2 || triage.Findings[0].ReviewerName != "valid" || triage.Findings[1].ReviewerName != "valid" {
		t.Fatalf("triage should include only the valid review, got %+v", triage.Findings)
	}
}

// TestOpenSessionRereadsConfig verifies that open_session reads the current
// project configuration on every call so stale review context cannot leak into
// later sessions.
func TestOpenSessionRereadsConfig(t *testing.T) {
	ctx, client, configPath := newTestClientWith(t, fixedReviewer{raw: validReviewOutput()}, baseTestConfig+"review_context: |\n  first calibration\n")

	first, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open session 1: %v", err)
	}
	if out := bindStructured[OpenSessionOutput](t, first); !out.ReviewContextPresent {
		t.Fatalf("first open: expected review_context_present, got %+v", out)
	}

	// drop review_context and the reviewer: open still succeeds and reports both
	// as absent.
	writeConfigFile(t, configPath, "log_destination: reviews\n")
	if err := os.Remove(testUserPath(configPath)); err != nil {
		t.Fatalf("remove user config: %v", err)
	}
	second, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open session 2: %v", err)
	}
	out2 := bindStructured[OpenSessionOutput](t, second)
	if out2.ReviewContextPresent || out2.Reviewer != nil {
		t.Fatalf("second open: expected review_context and reviewer absent after yaml edit, got %+v", out2)
	}

	// invalid yaml: open_session must surface a user_error instead of falling
	// back to a stale value.
	writeConfigFile(t, configPath, "this: is: not: valid: yaml\n\t::broken")
	third, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	payload := assertToolError(t, third, err, broker.CodeUserError)
	if !strings.Contains(payload.Message, "read mercurius.yaml") {
		t.Fatalf("expected 'read mercurius.yaml' message, got %+v", payload)
	}
}

// TestStartReviewRoundRereadsConfigLive is the core of the live-re-read change:
// a guard added to mercurius.yaml between rounds must appear in the very next
// round's prompt with no session reopen, and the round's _config.yaml must hold
// the guard as it was written.
func TestStartReviewRoundRereadsConfigLive(t *testing.T) {
	base := baseTestConfig
	ctx, client, configPath := newTestClientWith(t, fixedReviewer{raw: validReviewOutput()}, base)

	artifactPath := writeArtifact(t, "design.md", "# design\n")
	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := bindStructured[OpenSessionOutput](t, openResult).SessionID

	// round 1: no guard configured yet.
	round1 := startAndCollectRoundTool(t, ctx, client, sessionID, artifactPath)
	prompt1 := readFileContent(t, filepath.Join(filepath.Dir(round1.LogPath), "_prompt.md"))
	if strings.Contains(prompt1, "Settled decisions") {
		t.Fatalf("round 1 should have no settled-decisions block:\n%s", prompt1)
	}

	// add a guard between rounds, with no reopen.
	writeConfigFile(t, configPath, base+`settled_decisions:
  - id: recall-deferred
    do_not_flag: the absence of a 'recall' concept
`)

	round2 := startAndCollectRoundTool(t, ctx, client, sessionID, artifactPath)
	roundDir2 := filepath.Dir(round2.LogPath)
	prompt2 := readFileContent(t, filepath.Join(roundDir2, "_prompt.md"))
	if !strings.Contains(prompt2, "the absence of a 'recall' concept") {
		t.Fatalf("round 2 prompt missing the live-added guard:\n%s", prompt2)
	}
	cfg2 := readFileContent(t, filepath.Join(roundDir2, "_config.yaml"))
	if !strings.Contains(cfg2, "recall-deferred") {
		t.Fatalf("round 2 _config.yaml missing the guard source:\n%s", cfg2)
	}
}

// TestStartReviewRoundFailsCleanlyOnBrokenConfig: a round started against an
// unreadable YAML returns user_error, does not start a round, leaves the session
// active, and a retry after the file is fixed succeeds.
func TestStartReviewRoundFailsCleanlyOnBrokenConfig(t *testing.T) {
	valid := baseTestConfig
	ctx, client, configPath := newTestClientWith(t, fixedReviewer{raw: validReviewOutput()}, valid)

	artifactPath := writeArtifact(t, "design.md", "# design\n")
	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := bindStructured[OpenSessionOutput](t, openResult).SessionID

	// break the YAML between open and the round.
	writeConfigFile(t, configPath, "this: is: not: valid: yaml\n\t::broken")
	broken, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "start_review_round",
		Arguments: map[string]any{
			"session_id": sessionID,
			"artifacts":  []map[string]any{{"name": "design.md", "path": artifactPath}},
		},
	})
	payload := assertToolError(t, broken, err, broker.CodeUserError)
	if !strings.Contains(payload.Message, "read mercurius.yaml") {
		t.Fatalf("expected 'read mercurius.yaml' message, got %+v", payload)
	}

	// every tool resolves the config, so status answers the same user_error
	// until the file is fixed.
	statusResult, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "session_status", Arguments: map[string]any{"session_id": sessionID}})
	assertToolError(t, statusResult, err, broker.CodeUserError)

	// fix the file: the session is still active with no round recorded.
	writeConfigFile(t, configPath, valid)
	statusResult, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "session_status", Arguments: map[string]any{"session_id": sessionID}})
	if err != nil {
		t.Fatalf("session status: %v", err)
	}
	status := bindStructured[SessionStatusOutput](t, statusResult)
	if status.State != "active" {
		t.Fatalf("session state = %q, want active after a failed re-read", status.State)
	}
	if status.RoundCount != 0 {
		t.Fatalf("round count = %d, want 0 (round must not start)", status.RoundCount)
	}

	// retry; the round now runs.
	round := startAndCollectRoundTool(t, ctx, client, sessionID, artifactPath)
	if round.RoundNumber != 1 {
		t.Fatalf("retry round number = %d, want 1", round.RoundNumber)
	}
}

// TestRecordRoundNotesSchemaCarriesDispositionEnum asserts the registered
// record_round_notes input schema is explicit: the decisions array is fully
// typed and disposition carries the value enum. Reflection inference left
// disposition an unconstrained string, which is what hid the valid values from
// clients.
func TestRecordRoundNotesSchemaCarriesDispositionEnum(t *testing.T) {
	ctx, client := newTestClient(t)
	result, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	var schemaJSON []byte
	for _, tool := range result.Tools {
		if tool.Name == "record_round_notes" {
			schemaJSON, err = json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("marshal schema: %v", err)
			}
		}
	}
	if schemaJSON == nil {
		t.Fatal("record_round_notes not registered")
	}

	var s map[string]any
	if err := json.Unmarshal(schemaJSON, &s); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	items := mustMap(t, mustMap(t, mustMap(t, s, "properties"), "decisions"), "items")
	itemProps := mustMap(t, items, "properties")
	for _, field := range []string{"ref", "disposition", "note"} {
		if _, ok := itemProps[field]; !ok {
			t.Fatalf("decisions item missing property %q; schema=%s", field, schemaJSON)
		}
	}

	enumAny, ok := mustMap(t, itemProps, "disposition")["enum"].([]any)
	if !ok {
		t.Fatalf("disposition has no enum; schema=%s", schemaJSON)
	}
	got := make([]string, 0, len(enumAny))
	for _, v := range enumAny {
		got = append(got, v.(string))
	}
	if want := []string{"fixed", "rejected", "deferred"}; !slices.Equal(got, want) {
		t.Fatalf("disposition enum = %v, want %v", got, want)
	}

	required := make([]string, 0)
	if rawReq, ok := items["required"].([]any); ok {
		for _, v := range rawReq {
			required = append(required, v.(string))
		}
	}
	if !slices.Contains(required, "ref") || !slices.Contains(required, "disposition") {
		t.Fatalf("decisions item required = %v, want ref and disposition", required)
	}
}

// TestRecordRoundNotesPersistsDecisionsOnFirstCall is the regression guard for
// the dropped-decisions defect. The in-process path always recorded a
// well-formed decisions array; the field failure was a client malforming the
// first call against a schema that did not expose the disposition enum. With the
// explicit schema the first call lands, and an invalid disposition is rejected at
// the boundary rather than silently recording zero decisions.
func TestRecordRoundNotesPersistsDecisionsOnFirstCall(t *testing.T) {
	ctx, client := newTestClientWithFixedReviewer(t, reviewOutputWithConcernAndQuestion())
	sessionID, artifactPath := openTestSession(t, ctx, client)
	startAndCollectRoundTool(t, ctx, client, sessionID, artifactPath)

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "record_round_notes",
		Arguments: map[string]any{
			"session_id":   sessionID,
			"round_number": 1,
			"commentary":   "triaged",
			"decisions": []map[string]any{
				{"ref": "C-1", "disposition": "fixed", "note": "addressed"},
			},
		},
	})
	if err != nil {
		t.Fatalf("record notes: %v", err)
	}
	if result.IsError {
		t.Fatalf("first call errored: %+v", result.StructuredContent)
	}
	out := bindStructured[RecordRoundNotesOutput](t, result)
	if out.DecisionsRecorded != 1 {
		t.Fatalf("decisions recorded on first call = %d, want 1", out.DecisionsRecorded)
	}

	// an invalid disposition is rejected at the schema boundary, not silently
	// recorded as zero decisions.
	bad, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "record_round_notes",
		Arguments: map[string]any{
			"session_id":   sessionID,
			"round_number": 1,
			"decisions":    []map[string]any{{"ref": "C-1", "disposition": "accepted"}},
		},
	})
	if err != nil {
		t.Fatalf("invalid-disposition call protocol error: %v", err)
	}
	if bad == nil || !bad.IsError {
		t.Fatalf("expected invalid disposition to be rejected at the boundary, got %+v", bad)
	}
}

func TestSelectNextFindingBySeverity(t *testing.T) {
	mk := func(ref string, kind string, severity *string) TriageFindingOutput {
		return TriageFindingOutput{Ref: ref, Kind: kind, Severity: severity}
	}
	sev := func(s string) *string { return &s }

	tests := []struct {
		name     string
		findings []TriageFindingOutput
		wantRef  string
	}{
		{
			name: "blocker beats major even when major is first",
			findings: []TriageFindingOutput{
				mk("C-1", "concern", sev("major")),
				mk("C-2", "concern", sev("blocker")),
				mk("C-3", "concern", sev("minor")),
			},
			wantRef: "C-2",
		},
		{
			name: "concern wins over question",
			findings: []TriageFindingOutput{
				mk("Q-1", "question", nil),
				mk("C-9", "concern", sev("minor")),
			},
			wantRef: "C-9",
		},
		{
			name: "questions sort by id when no concerns are present",
			findings: []TriageFindingOutput{
				mk("Q-3", "question", nil),
				mk("Q-1", "question", nil),
				mk("Q-2", "question", nil),
			},
			wantRef: "Q-1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := selectNextFinding(test.findings)
			if got.Ref != test.wantRef {
				t.Fatalf("next finding ref = %q, want %q", got.Ref, test.wantRef)
			}
		})
	}
}

func TestCollectRoundTriageAdvisoryNotes(t *testing.T) {
	ctx, client := newTestClientWithFixedReviewer(t, reviewOutputWithAdvisoryOnly())
	sessionID, artifactPath := openTestSession(t, ctx, client)

	collected := startAndCollectRoundTool(t, ctx, client, sessionID, artifactPath)
	if collected.Triage.TotalFindings != 0 || len(collected.Triage.Findings) != 0 || collected.Triage.NextFinding != nil {
		t.Fatalf("blocking triage = %+v", collected.Triage)
	}
	if len(collected.Triage.AdvisoryNotes) != 1 {
		t.Fatalf("advisory notes = %+v", collected.Triage.AdvisoryNotes)
	}
	note := collected.Triage.AdvisoryNotes[0]
	if note.ReviewerName != "triage" || note.Ref != "A-1" || note.Note != "shorten one paragraph" || note.Rationale != "easier to scan" {
		t.Fatalf("advisory note = %+v", note)
	}
	if !strings.Contains(collected.NextAction, "no blocking findings") {
		t.Fatalf("next action = %q", collected.NextAction)
	}
}

// baseTestConfig is a project mercurius.yaml. The reviewer lives in a
// user-level file beside it, written by newTestClientWith; no test reads the
// developer's real user-level config.
const baseTestConfig = "log_destination: reviews\n"

const baseTestUser = `reviewer:
  name: dummy
  impl: dummy
`

func newTestClient(t *testing.T) (context.Context, *mcp.ClientSession) {
	t.Helper()
	ctx, client, _ := newTestClientWith(t, fixedReviewer{raw: validReviewOutput()}, baseTestConfig)
	return ctx, client
}

func newTestClientWithFixedReviewer(t *testing.T, raw json.RawMessage) (context.Context, *mcp.ClientSession) {
	t.Helper()
	ctx, client, configPath := newTestClientWith(t, fixedReviewer{raw: raw}, baseTestConfig)
	writeConfigFile(t, testUserPath(configPath), strings.Replace(baseTestUser, "name: dummy", "name: triage", 1))
	return ctx, client
}

// testUserPath is the user-level config newTestClientWith writes beside the
// project file.
func testUserPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "user.yaml")
}

// newTestClientWith writes a project mercurius.yaml and serves it through a
// broker whose rounds run in process with r as the reviewer. It returns the
// config path so a test can edit the file between calls.
func newTestClientWith(t *testing.T, r reviewer.Reviewer, configBody string) (context.Context, *mcp.ClientSession, string) {
	t.Helper()
	launcher := runner.NewInProcess(func(store.RunReviewer) (reviewer.Reviewer, error) { return r, nil })
	return newTestClientWithLauncher(t, launcher, configBody)
}

func newTestClientWithLauncher(t *testing.T, launcher *runner.InProcess, configBody string) (context.Context, *mcp.ClientSession, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mercurius.yaml")
	writeConfigFile(t, configPath, configBody)
	writeConfigFile(t, testUserPath(configPath), baseTestUser)
	t.Cleanup(launcher.Wait)
	b := broker.New(broker.Options{
		ConfigPath:     configPath,
		UserConfigPath: testUserPath(configPath),
		Launcher:       launcher,
	})
	server := mcp.NewServer(&mcp.Implementation{Name: "test-project", Version: Version}, nil)
	if err := RegisterTools(server, b); err != nil {
		t.Fatalf("register tools: %v", err)
	}
	ctx, client := newTestClientForServer(t, server)
	return ctx, client, configPath
}

func newTestClientForServer(t *testing.T, server *mcp.Server) (context.Context, *mcp.ClientSession) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	return ctx, clientSession
}

func openTestSession(t *testing.T, ctx context.Context, client *mcp.ClientSession) (string, string) {
	t.Helper()

	artifactPath := writeArtifact(t, "design.md", "# design\n")
	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "open_session",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	return bindStructured[OpenSessionOutput](t, openResult).SessionID, artifactPath
}

func startAndCollectRoundTool(t *testing.T, ctx context.Context, client *mcp.ClientSession, sessionID string, artifactPath string) CollectedRoundOutput {
	t.Helper()
	started := startRoundTool(t, ctx, client, sessionID, artifactPath)
	return collectRound(t, ctx, client, sessionID, started.RoundNumber)
}

func startRoundTool(t *testing.T, ctx context.Context, client *mcp.ClientSession, sessionID string, artifactPath string) StartReviewRoundOutput {
	t.Helper()

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "start_review_round",
		Arguments: map[string]any{
			"session_id": sessionID,
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
		},
	})
	if err != nil {
		t.Fatalf("start review round: %v", err)
	}
	if result.IsError {
		t.Fatalf("start review round returned tool error: %+v", result.StructuredContent)
	}
	return bindStructured[StartReviewRoundOutput](t, result)
}

func collectRound(t *testing.T, ctx context.Context, client *mcp.ClientSession, sessionID string, roundNumber int) CollectedRoundOutput {
	t.Helper()

	deadline := time.After(5 * time.Second)
	for {
		result, err := client.CallTool(ctx, &mcp.CallToolParams{
			Name: "collect_round",
			Arguments: map[string]any{
				"session_id":   sessionID,
				"round_number": roundNumber,
			},
		})
		if err != nil {
			t.Fatalf("collect round: %v", err)
		}
		if !result.IsError {
			return bindStructured[CollectedRoundOutput](t, result)
		}
		payload := bindStructured[ToolErrorOutput](t, result)
		if payload.Error.Code != broker.CodeConflict {
			t.Fatalf("collect round returned tool error: %+v", payload.Error)
		}
		select {
		case <-deadline:
			t.Fatalf("collect round did not complete; last error=%+v", payload.Error)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func startAndCollectRoundToolError(t *testing.T, ctx context.Context, client *mcp.ClientSession, sessionID string, artifactPath string, wantStableCode string) ErrorOutput {
	t.Helper()

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "start_review_round",
		Arguments: map[string]any{
			"session_id": sessionID,
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
		},
	})
	if err != nil {
		t.Fatalf("start review round: %v", err)
	}
	if result.IsError {
		return assertToolError(t, result, nil, wantStableCode)
	}
	started := bindStructured[StartReviewRoundOutput](t, result)

	deadline := time.After(5 * time.Second)
	for {
		result, err = client.CallTool(ctx, &mcp.CallToolParams{
			Name: "collect_round",
			Arguments: map[string]any{
				"session_id":   sessionID,
				"round_number": started.RoundNumber,
			},
		})
		if err != nil {
			t.Fatalf("collect round: %v", err)
		}
		if result.IsError {
			payload := bindStructured[ToolErrorOutput](t, result)
			if payload.Error.Code == broker.CodeConflict {
				select {
				case <-deadline:
					t.Fatalf("collect round did not reach error %s; last error=%+v", wantStableCode, payload.Error)
				case <-time.After(10 * time.Millisecond):
				}
				continue
			}
			return assertToolError(t, result, nil, wantStableCode)
		}
		t.Fatalf("collect round succeeded while waiting for error %s", wantStableCode)
	}
}

func bindStructured[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()

	if result == nil {
		t.Fatal("nil tool result")
	}
	data, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected object in structured content, got %T", result.StructuredContent)
	}
	var out T
	if err := dd.Bind(&out, data); err != nil {
		t.Fatalf("bind structured content: %v", err)
	}
	return out
}

func assertToolError(t *testing.T, result *mcp.CallToolResult, err error, wantStableCode string) ErrorOutput {
	t.Helper()

	if err != nil {
		t.Fatalf("expected tool error result, got protocol error: %v", err)
	}
	if result == nil {
		t.Fatal("nil tool result")
	}
	if !result.IsError {
		t.Fatalf("expected IsError result, got %+v", result)
	}
	payload := bindStructured[ToolErrorOutput](t, result)
	if payload.Error.Code != wantStableCode {
		t.Fatalf("stable code = %s, want %s; payload=%+v", payload.Error.Code, wantStableCode, payload)
	}
	return payload.Error
}

func mustMap(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("expected %q to be an object, got %T", key, m[key])
	}
	return v
}

func writeConfigFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readFileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeArtifact(t *testing.T, name string, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

type failingReviewer struct{}

func (failingReviewer) Review(context.Context, reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	return reviewer.ReviewResponse{}, errors.New("reviewer unavailable")
}

type fixedReviewer struct {
	raw json.RawMessage
}

func (r fixedReviewer) Review(ctx context.Context, _ reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return reviewer.ReviewResponse{}, err
	}
	return reviewer.ReviewResponse{Raw: append(json.RawMessage(nil), r.raw...), UsageNotes: "fixed"}, nil
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

func (r *blockingReviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
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

func waitBlockingStarted(t *testing.T, r *blockingReviewer) {
	t.Helper()
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not start")
	}
}

func validReviewOutput() json.RawMessage {
	return json.RawMessage(wireReviewWithoutFindings)
}

func reviewOutputWithConcernAndQuestion() json.RawMessage {
	return json.RawMessage(`{
		"verdict": "needs_changes",
		"summary": "needs one clarification and one answer",
		"concerns": [{
			"id": "C-1",
			"severity": "major",
			"location": "docs/design.md",
			"claim": "scope is ambiguous",
			"rationale": "implementation would diverge",
			"suggestion": "make the acceptance criteria explicit"
		}],
		"questions": [{
			"id": "Q-1",
			"topic": "rollout",
			"why_it_blocks": "cannot determine deployment order"
		}],
		"advisory_notes": []
	}`)
}

func reviewOutputWithAdvisoryOnly() json.RawMessage {
	return json.RawMessage(`{
		"verdict": "ready_to_build",
		"summary": "ready with optional polish",
		"concerns": [],
		"questions": [],
		"advisory_notes": [{
			"id": "A-1",
			"location": "docs/design.md",
			"note": "shorten one paragraph",
			"rationale": "easier to scan",
			"suggestion": "trim the second paragraph"
		}]
	}`)
}

// inProcessLauncher runs rounds in this test process with the production
// reviewer factory. The production process launcher would start this test
// binary as the runner.
func inProcessLauncher(t *testing.T) *runner.InProcess {
	t.Helper()
	launcher := runner.NewInProcess(runner.BuildReviewer)
	t.Cleanup(launcher.Wait)
	return launcher
}

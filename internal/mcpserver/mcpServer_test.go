package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/schema"
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
	openOutput := decodeStructured[OpenSessionOutput](t, openResult)
	if openOutput.SessionID == "" {
		t.Fatal("expected session id")
	}
	if openOutput.MaxFindings != config.DefaultMaxFindings {
		t.Fatalf("max findings = %d, want %d", openOutput.MaxFindings, config.DefaultMaxFindings)
	}
	if openOutput.Reviewer.Name != "dummy" {
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
	notesOutput := decodeStructured[RecordRoundNotesOutput](t, notesResult)
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
	statusOutput := decodeStructured[SessionStatusOutput](t, statusResult)
	if statusOutput.RoundCount != 1 || len(statusOutput.Rounds) != 1 || !statusOutput.Rounds[0].HasNotes {
		t.Fatalf("status output = %+v", statusOutput)
	}
	if statusOutput.Reviewer.Name != "dummy" || statusOutput.LastError != nil {
		t.Fatalf("status diagnostics = %+v", statusOutput)
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
	closeOutput := decodeStructured[CloseSessionOutput](t, closeResult)
	if closeOutput.SessionID != openOutput.SessionID || closeOutput.ClosedAt == "" {
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
	sessionID := decodeStructured[OpenSessionOutput](t, openResult).SessionID

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

func TestFailedRoundReturnsToolErrorAndStatusLastError(t *testing.T) {
	logDestination := filepath.Join(t.TempDir(), "reviews")
	b := broker.New(broker.Options{
		LogDestination: logDestination,
		Reviewer:       failingReviewer{},
		ReviewerInfo:   broker.ReviewerInfo{Name: "failing", Impl: "dummy"},
	})
	ctx, client := newTestClientWithBroker(t, b)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "open_session",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sessionID := decodeStructured[OpenSessionOutput](t, openResult).SessionID

	startAndCollectRoundToolError(t, ctx, client, sessionID, artifactPath, broker.CodeReviewerFailed)

	statusResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "session_status",
		Arguments: map[string]any{
			"session_id": sessionID,
		},
	})
	if err != nil {
		t.Fatalf("session status: %v", err)
	}
	status := decodeStructured[SessionStatusOutput](t, statusResult)
	if status.RoundCount != 0 {
		t.Fatalf("round count after failure = %d, want 0", status.RoundCount)
	}
	if status.LastError == nil || status.LastError.Code != broker.CodeReviewerFailed {
		t.Fatalf("last error = %+v", status.LastError)
	}
}

func TestAsyncReviewTools(t *testing.T) {
	reviewerImpl := newBlockingReviewer(validReviewOutput())
	b := broker.New(broker.Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		MaxFindings:    config.DefaultMaxFindings,
		Reviewer:       reviewerImpl,
		ReviewerInfo:   broker.ReviewerInfo{Name: "blocking", Impl: "dummy"},
	})
	ctx, client := newTestClientWithBroker(t, b)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "open_session",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sessionID := decodeStructured[OpenSessionOutput](t, openResult).SessionID

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
	start := decodeStructured[StartReviewRoundOutput](t, startResult)
	if start.RoundNumber != 1 || start.State != "running" || start.MonitorCommand == "" {
		t.Fatalf("start output = %+v", start)
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

	reviewerImpl.release()
	var collected CollectedRoundOutput
	deadline := time.After(time.Second)
	for {
		collectResult, err = client.CallTool(ctx, &mcp.CallToolParams{
			Name: "collect_round",
			Arguments: map[string]any{
				"session_id":   sessionID,
				"round_number": 1,
			},
		})
		if err == nil && collectResult != nil && !collectResult.IsError {
			collected = decodeStructured[CollectedRoundOutput](t, collectResult)
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
	ctx, client := newTestClientWithFixedReviewer(t, reviewOutputWithConcernAndQuestion(t))
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

// TestOpenSessionRereadsConfig guards the cross-talk regression where
// review_context was cached at server startup and leaked into later sessions
// after mercurius.yaml had been edited. ConfigCalibrationProvider must observe
// the current YAML on every open_session call.
func TestOpenSessionRereadsConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig := func(body string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	writeConfig(`log_destination: ` + filepath.Join(dir, "reviews") + `
review_context: |
  first calibration
reviewer:
  name: dummy
  impl: dummy
`)

	b := broker.New(broker.Options{
		LogDestination: filepath.Join(dir, "reviews"),
		MaxFindings:    config.DefaultMaxFindings,
		Reviewer:       fixedReviewer{raw: validReviewOutput()},
		ReviewerInfo:   broker.ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})
	server := mcp.NewServer(&mcp.Implementation{Name: "test-project", Version: Version}, nil)
	RegisterTools(server, b, ConfigCalibrationProvider(configPath))
	ctx, client := newTestClientForServer(t, server)

	// first open: context present
	first, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open session 1: %v", err)
	}
	out1 := decodeStructured[OpenSessionOutput](t, first)
	if !out1.ReviewContextPresent {
		t.Fatalf("first open: expected review_context_present, got %+v", out1)
	}

	// edit the yaml to drop review_context entirely
	writeConfig(`log_destination: ` + filepath.Join(dir, "reviews") + `
reviewer:
  name: dummy
  impl: dummy
`)
	second, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open session 2: %v", err)
	}
	out2 := decodeStructured[OpenSessionOutput](t, second)
	if out2.ReviewContextPresent {
		t.Fatalf("second open: expected review_context absent after yaml edit, got %+v", out2)
	}

	// invalid yaml: open_session must surface a user_error instead of falling
	// back to a stale value.
	writeConfig("this: is: not: valid: yaml\n\t::broken")
	third, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open session 3: %v", err)
	}
	payload := assertToolError(t, third, nil, broker.CodeUserError)
	if !strings.Contains(payload.Message, "reread mercurius.yaml") {
		t.Fatalf("expected 'reread mercurius.yaml' message, got %+v", payload)
	}
}

// TestStartReviewRoundRereadsConfigLive is the core of the live-re-read change:
// a guard added to mercurius.yaml between rounds must appear in the very next
// round's prompt with no session reopen, and the round's _config.yaml must hold
// the guard as it was written.
func TestStartReviewRoundRereadsConfigLive(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mercurius.yaml")
	base := `log_destination: ` + filepath.Join(dir, "reviews") + `
reviewer:
  name: dummy
  impl: dummy
`
	writeConfigFile(t, configPath, base)

	b := broker.New(broker.Options{
		LogDestination: filepath.Join(dir, "reviews"),
		MaxFindings:    config.DefaultMaxFindings,
		Reviewer:       fixedReviewer{raw: validReviewOutput()},
		ReviewerInfo:   broker.ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})
	server := mcp.NewServer(&mcp.Implementation{Name: "test-project", Version: Version}, nil)
	RegisterTools(server, b, ConfigCalibrationProvider(configPath))
	ctx, client := newTestClientForServer(t, server)

	artifactPath := writeArtifact(t, "design.md", "# design\n")
	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := decodeStructured[OpenSessionOutput](t, openResult).SessionID

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
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mercurius.yaml")
	valid := `log_destination: ` + filepath.Join(dir, "reviews") + `
reviewer:
  name: dummy
  impl: dummy
`
	writeConfigFile(t, configPath, valid)

	b := broker.New(broker.Options{
		LogDestination: filepath.Join(dir, "reviews"),
		MaxFindings:    config.DefaultMaxFindings,
		Reviewer:       fixedReviewer{raw: validReviewOutput()},
		ReviewerInfo:   broker.ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})
	server := mcp.NewServer(&mcp.Implementation{Name: "test-project", Version: Version}, nil)
	RegisterTools(server, b, ConfigCalibrationProvider(configPath))
	ctx, client := newTestClientForServer(t, server)

	artifactPath := writeArtifact(t, "design.md", "# design\n")
	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := decodeStructured[OpenSessionOutput](t, openResult).SessionID

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
	if !strings.Contains(payload.Message, "reread mercurius.yaml") {
		t.Fatalf("expected 'reread mercurius.yaml' message, got %+v", payload)
	}

	// the session must still be active with no round recorded.
	statusResult, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "session_status", Arguments: map[string]any{"session_id": sessionID}})
	if err != nil {
		t.Fatalf("session status: %v", err)
	}
	status := decodeStructured[SessionStatusOutput](t, statusResult)
	if status.State != "active" {
		t.Fatalf("session state = %q, want active after a failed re-read", status.State)
	}
	if status.RoundCount != 0 {
		t.Fatalf("round count = %d, want 0 (round must not start)", status.RoundCount)
	}

	// fix the file and retry; the round now runs.
	writeConfigFile(t, configPath, valid)
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
	ctx, client := newTestClientWithFixedReviewer(t, reviewOutputWithConcernAndQuestion(t))
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
	out := decodeStructured[RecordRoundNotesOutput](t, result)
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
	ctx, client := newTestClientWithFixedReviewer(t, reviewOutputWithAdvisoryOnly(t))
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

func newTestClient(t *testing.T) (context.Context, *mcp.ClientSession) {
	t.Helper()

	b := broker.New(broker.Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		MaxFindings:    config.DefaultMaxFindings,
		Reviewer:       fixedReviewer{raw: validReviewOutput()},
		ReviewerInfo:   broker.ReviewerInfo{Name: "dummy", Impl: "dummy"},
	})
	return newTestClientWithBroker(t, b)
}

func newTestClientWithFixedReviewer(t *testing.T, raw json.RawMessage) (context.Context, *mcp.ClientSession) {
	t.Helper()

	b := broker.New(broker.Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		MaxFindings:    config.DefaultMaxFindings,
		Reviewer:       fixedReviewer{raw: raw},
		ReviewerInfo:   broker.ReviewerInfo{Name: "triage", Impl: "dummy"},
	})
	return newTestClientWithBroker(t, b)
}

func newTestClientWithBroker(t *testing.T, b *broker.Broker) (context.Context, *mcp.ClientSession) {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-project", Version: Version}, nil)
	RegisterTools(server, b, nil)
	return newTestClientForServer(t, server)
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
	return decodeStructured[OpenSessionOutput](t, openResult).SessionID, artifactPath
}

func startAndCollectRoundTool(t *testing.T, ctx context.Context, client *mcp.ClientSession, sessionID string, artifactPath string) CollectedRoundOutput {
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
	started := decodeStructured[StartReviewRoundOutput](t, result)
	return collectRound(t, ctx, client, sessionID, started.RoundNumber)
}

func collectRound(t *testing.T, ctx context.Context, client *mcp.ClientSession, sessionID string, roundNumber int) CollectedRoundOutput {
	t.Helper()

	deadline := time.After(time.Second)
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
			return decodeStructured[CollectedRoundOutput](t, result)
		}
		payload := decodeStructured[ToolErrorOutput](t, result)
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
	started := decodeStructured[StartReviewRoundOutput](t, result)

	deadline := time.After(time.Second)
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
			payload := decodeStructured[ToolErrorOutput](t, result)
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

func decodeStructured[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()

	if result == nil {
		t.Fatal("nil tool result")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content: %v; raw=%s", err, string(raw))
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
	payload := decodeStructured[ToolErrorOutput](t, result)
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
	started  chan struct{}
	releaseC chan struct{}
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
	close(r.started)
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

func waitBlockingStarted(t *testing.T, r *blockingReviewer) {
	t.Helper()
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("reviewer did not start")
	}
}

func validReviewOutput() json.RawMessage {
	raw, err := json.Marshal(schema.ReviewOutput{
		Verdict:       "ready_to_build",
		Summary:       "ready",
		Concerns:      []schema.Concern{},
		Questions:     []schema.Question{},
		AdvisoryNotes: []schema.AdvisoryNote{},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func reviewOutputWithConcernAndQuestion(t *testing.T) json.RawMessage {
	t.Helper()

	suggestion := "make the acceptance criteria explicit"
	raw, err := json.Marshal(schema.ReviewOutput{
		Verdict: "needs_changes",
		Summary: "needs one clarification and one answer",
		Concerns: []schema.Concern{{
			ID:         "C-1",
			Severity:   "major",
			Location:   "docs/design.md",
			Claim:      "scope is ambiguous",
			Rationale:  "implementation would diverge",
			Suggestion: &suggestion,
		}},
		Questions: []schema.Question{{
			ID:          "Q-1",
			Topic:       "rollout",
			WhyItBlocks: "cannot determine deployment order",
		}},
		AdvisoryNotes: []schema.AdvisoryNote{},
	})
	if err != nil {
		t.Fatalf("marshal concern/question review output: %v", err)
	}
	return raw
}

func reviewOutputWithAdvisoryOnly(t *testing.T) json.RawMessage {
	t.Helper()

	suggestion := "trim the second paragraph"
	raw, err := json.Marshal(schema.ReviewOutput{
		Verdict:   "ready_to_build",
		Summary:   "ready with optional polish",
		Concerns:  []schema.Concern{},
		Questions: []schema.Question{},
		AdvisoryNotes: []schema.AdvisoryNote{{
			ID:         "A-1",
			Location:   "docs/design.md",
			Note:       "shorten one paragraph",
			Rationale:  "easier to scan",
			Suggestion: &suggestion,
		}},
	})
	if err != nil {
		t.Fatalf("marshal advisory review output: %v", err)
	}
	return raw
}

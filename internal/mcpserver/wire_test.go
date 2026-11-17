package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/runner"
	"github.com/michaelquigley/mercurius/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const wireReviewWithoutFindings = `{
  "verdict": "ready_to_build",
  "summary": "ready",
  "concerns": [],
  "questions": [],
  "advisory_notes": []
}`

const wireReviewWithFindings = `{
  "verdict": "needs_changes",
  "summary": "one clarification and one answer",
  "concerns": [{
    "id": "C-1",
    "severity": "major",
    "location": "design.md",
    "claim": "scope is ambiguous",
    "rationale": "implementation would diverge",
    "suggestion": "state the acceptance criteria"
  }],
  "questions": [{
    "id": "Q-1",
    "topic": "rollout",
    "why_it_blocks": "deployment order is unclear"
  }],
  "advisory_notes": [{
    "id": "A-1",
    "location": "design.md",
    "note": "shorten the introduction",
    "rationale": "easier to scan",
    "suggestion": null
  }]
}`

type wireRoundReviewers map[int]reviewer.Reviewer

func (r wireRoundReviewers) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	impl, ok := r[req.SessionMeta.RoundNumber]
	if !ok {
		return reviewer.ReviewResponse{}, fmt.Errorf("no test reviewer for round '%d'", req.SessionMeta.RoundNumber)
	}
	return impl.Review(ctx, req)
}

func TestWireLifecycle(t *testing.T) {
	blocked := newBlockingReviewer(json.RawMessage(wireReviewWithoutFindings))
	reviewers := wireRoundReviewers{
		1: fixedReviewer{raw: json.RawMessage(wireReviewWithFindings)},
		2: failingReviewer{},
		3: blocked,
	}
	launcher := runner.NewInProcess(func(store.RunReviewer) (reviewer.Reviewer, error) { return reviewers, nil })
	ctx, client, configPath := newTestClientWithLauncher(t, launcher, baseTestConfig+"review_context: wire fixture\n")
	t.Cleanup(blocked.release)
	projectDir := filepath.Dir(configPath)
	artifactPath := filepath.Join(projectDir, "design.md")
	if err := os.WriteFile(artifactPath, []byte("# design\n"), 0o600); err != nil {
		t.Fatalf("write design: %v", err)
	}

	opened := callWireTool(t, ctx, client, "open_session", map[string]any{})
	sessionID := wireSessionID(t, opened)
	normalizer := wireNormalizer{replacements: strings.NewReplacer(projectDir, "<project>", sessionID, "<session>")}
	logDir := filepath.Join(projectDir, "reviews")
	sessionDir := store.SessionDir(logDir, sessionID)
	sessionPath := filepath.Join(sessionDir, store.SessionFileName)

	checkResult := func(name string, result *mcp.CallToolResult) {
		t.Helper()
		if result.IsError || result.StructuredContent == nil {
			t.Fatalf("expected structured result for '%s', got %+v", name, result)
		}
		checkWireGolden(t, "results/"+name, normalizer.structured(t, result))
	}
	checkError := func(code string, result *mcp.CallToolResult) {
		t.Helper()
		assertToolError(t, result, nil, code)
		checkWireGolden(t, "errors/"+code, normalizer.structured(t, result))
	}
	checkFile := func(name string, path string) {
		t.Helper()
		checkWireGolden(t, "files/"+name, normalizer.file(t, path))
	}
	start := func() *mcp.CallToolResult {
		t.Helper()
		return callWireTool(t, ctx, client, "start_review_round", map[string]any{
			"session_id": sessionID,
			"artifacts":  []map[string]string{{"name": "design.md", "path": artifactPath}},
		})
	}
	collect := func(round int) *mcp.CallToolResult {
		t.Helper()
		return callWireTool(t, ctx, client, "collect_round", map[string]any{
			"session_id":   sessionID,
			"round_number": round,
		})
	}

	checkResult("open_session", opened)
	checkFile("session_open", sessionPath)
	checkError(broker.CodeNotFound, callWireTool(t, ctx, client, "session_status", map[string]any{
		"session_id": "s_missing",
	}))
	checkError(broker.CodeUserError, callWireTool(t, ctx, client, "start_review_round", map[string]any{
		"session_id": sessionID,
		"artifacts":  nil,
	}))

	checkResult("start_review_round", start())
	launcher.Wait()
	checkResult("collect_round_findings", collect(1))
	checkResult("record_round_notes", callWireTool(t, ctx, client, "record_round_notes", map[string]any{
		"session_id":   sessionID,
		"round_number": 1,
		"commentary":   "reviewed both findings",
		"decisions": []map[string]string{
			{"ref": "C-1", "disposition": "fixed"},
			{"ref": "Q-1", "disposition": "deferred", "note": "cover in rollout"},
		},
	}))
	completedDir := store.RoundDir(sessionDir, 1)
	checkFile("run", filepath.Join(completedDir, store.RunFileName))
	checkFile("state_completed", filepath.Join(completedDir, store.StateFileName))
	checkFile("review", filepath.Join(completedDir, store.ReviewFileName))
	checkFile("notes", filepath.Join(completedDir, store.NotesJSONFileName))

	requireWireRound(t, start(), 2)
	launcher.Wait()
	checkError(broker.CodeReviewerFailed, collect(2))
	checkFile("state_failed", filepath.Join(store.RoundDir(sessionDir, 2), store.StateFileName))

	requireWireRound(t, start(), 3)
	waitBlockingStarted(t, blocked)
	checkFile("state_running", filepath.Join(store.RoundDir(sessionDir, 3), store.StateFileName))
	checkFile("runner_lock_note", store.LockPath(logDir))
	checkError(broker.CodeConflict, start())
	checkResult("session_status", callWireTool(t, ctx, client, "session_status", map[string]any{"session_id": sessionID}))
	blocked.release()
	launcher.Wait()
	checkResult("collect_round_no_findings", collect(3))
	checkResult("close_session", callWireTool(t, ctx, client, "close_session", map[string]any{"session_id": sessionID}))
	checkFile("session_closed", sessionPath)

	writeConfigFile(t, testUserPath(configPath), "{}\n")
	withoutReviewer := callWireTool(t, ctx, client, "open_session", map[string]any{})
	normalizer.replacements = strings.NewReplacer(projectDir, "<project>", wireSessionID(t, withoutReviewer), "<session>")
	checkResult("open_session_no_reviewer", withoutReviewer)
}

func TestWireFailedStateWithoutDetails(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	state := store.State{
		State:       store.RoundFailed,
		StartedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
		Error:       store.NewStoredError(store.CodeReviewerFailed, "reviewer failed", "", nil, now),
	}
	if err := store.WriteState(dir, state); err != nil {
		t.Fatalf("write failed state: %v", err)
	}
	checkWireGolden(t, "files/state_failed_no_details", (wireNormalizer{}).file(t, filepath.Join(dir, store.StateFileName)))
}

func TestWireInputSchemas(t *testing.T) {
	ctx, client := newTestClient(t)
	result, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range result.Tools {
		checkWireGolden(t, "schemas/"+tool.Name, decodeWireJSON(t, marshalWireJSON(t, tool.InputSchema)))
	}
}

func callWireTool(t *testing.T, ctx context.Context, client *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call '%s': %v", name, err)
	}
	return result
}

func wireSessionID(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok || result.IsError {
		t.Fatalf("expected open_session result, got %+v", result)
	}
	sessionID, ok := payload["session_id"].(string)
	if !ok || !store.ValidSessionID(sessionID) {
		t.Fatalf("invalid session id '%v'", payload["session_id"])
	}
	return sessionID
}

func requireWireRound(t *testing.T, result *mcp.CallToolResult, want int) {
	t.Helper()
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok || result.IsError || payload["round_number"] != float64(want) {
		t.Fatalf("expected result for round '%d', got %+v", want, result)
	}
}

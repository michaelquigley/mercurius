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
	ctx, client := newTestClient(t, testConfig(t, dummyReviewer()))

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
		"list_reviewers",
		"list_sessions",
		"open_session",
		"record_round_notes",
		"review_round",
		"round_status",
		"session_status",
		"start_review_round",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestDummyReviewFlow(t *testing.T) {
	ctx, client := newTestClient(t, testConfig(t, dummyReviewer()))
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
		},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	openOutput := decodeStructured[OpenSessionOutput](t, openResult)
	if openOutput.SessionID == "" {
		t.Fatal("expected session id")
	}
	if openOutput.Budget != 2 {
		t.Fatalf("budget = %d, want 2", openOutput.Budget)
	}
	if openOutput.BudgetRemaining != 2 || openOutput.RoundsUsed != 0 || openOutput.MaxFindings != config.DefaultMaxFindings {
		t.Fatalf("open budget state = %+v", openOutput)
	}
	if len(openOutput.Reviewers) != 1 || openOutput.Reviewers[0].Name != "dummy" {
		t.Fatalf("open reviewers = %+v", openOutput.Reviewers)
	}
	if len(openOutput.Artifacts) != 1 || openOutput.Artifacts[0].Name != "design.md" {
		t.Fatalf("open artifacts = %+v", openOutput.Artifacts)
	}

	round1 := callRound(t, ctx, client, openOutput.SessionID)
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
	if !strings.Contains(notesOutput.NextAction, "pause") {
		t.Fatalf("notes next action = %q", notesOutput.NextAction)
	}

	round2 := callRound(t, ctx, client, openOutput.SessionID)
	if round2.RoundNumber != 2 {
		t.Fatalf("round number = %d, want 2", round2.RoundNumber)
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
	if statusOutput.RoundsUsed != 2 || statusOutput.BudgetRemaining != 0 || statusOutput.MaxFindings != config.DefaultMaxFindings || len(statusOutput.Rounds) != 2 || !statusOutput.Rounds[0].HasNotes {
		t.Fatalf("status output = %+v", statusOutput)
	}
	if len(statusOutput.Reviewers) != 1 || len(statusOutput.Artifacts) != 1 || statusOutput.LastError != nil {
		t.Fatalf("status diagnostics = %+v", statusOutput)
	}

	listResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_sessions",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	listOutput := decodeStructured[ListSessionsOutput](t, listResult)
	if len(listOutput.Sessions) != 1 || listOutput.Sessions[0].SessionID != openOutput.SessionID {
		t.Fatalf("list output = %+v", listOutput)
	}

	closeResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "close_session",
		Arguments: map[string]any{
			"session_id": openOutput.SessionID,
			"verdict":    "ready_to_build",
		},
	})
	if err != nil {
		t.Fatalf("close session: %v", err)
	}
	closeOutput := decodeStructured[CloseSessionOutput](t, closeResult)
	if closeOutput.Verdict != "ready_to_build" || closeOutput.ClosedAt == "" {
		t.Fatalf("close output = %+v", closeOutput)
	}
}

func TestBrokerErrorsBecomeToolErrors(t *testing.T) {
	ctx, client := newTestClient(t, testConfig(t, dummyReviewer()))

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "session_status",
		Arguments: map[string]any{
			"session_id": "s_missing",
		},
	})
	assertToolError(t, result, err, broker.CodeUnknownSession)
}

func TestListReviewers(t *testing.T) {
	cfg := testConfig(t, &config.ReviewerConfig{Name: "codex", Impl: "codex", Model: "gpt-test"}, dummyReviewer())
	ctx, client := newTestClient(t, cfg)

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_reviewers",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("list reviewers: %v", err)
	}
	output := decodeStructured[ListReviewersOutput](t, result)
	if len(output.Reviewers) != 2 {
		t.Fatalf("reviewers = %+v", output.Reviewers)
	}
	if output.Reviewers[0].Name != "codex" || output.Reviewers[0].Model != "gpt-test" || !output.Reviewers[0].Selectable {
		t.Fatalf("first reviewer = %+v", output.Reviewers[0])
	}
}

func TestReviewerSelection(t *testing.T) {
	cfg := testConfig(t, &config.ReviewerConfig{Name: "codex", Impl: "codex"}, dummyReviewer())
	ctx, client := newTestClient(t, cfg)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
		},
	})
	assertToolError(t, result, err, broker.CodePanelModeUnsupported)

	result, err = client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
			"reviewers": []string{"codex"},
		},
	})
	if err != nil {
		t.Fatalf("open session with codex selection: %v", err)
	}
	if output := decodeStructured[OpenSessionOutput](t, result); output.SessionID == "" {
		t.Fatal("expected session id")
	}

	result, err = client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
			"reviewers": []string{"codex", "dummy"},
		},
	})
	assertToolError(t, result, err, broker.CodePanelModeUnsupported)

	result, err = client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
			"reviewers": []string{"missing"},
		},
	})
	assertToolError(t, result, err, broker.CodeUnknownReviewer)
}

func TestArtifactNameValidation(t *testing.T) {
	ctx, client := newTestClient(t, testConfig(t, dummyReviewer()))
	artifactPath := writeArtifact(t, "design.md", "# design\n")

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
				Name: "open_session",
				Arguments: map[string]any{
					"artifacts": []map[string]any{{
						"name": name,
						"path": artifactPath,
					}},
				},
			})
			assertToolError(t, result, err, broker.CodeInvalidArtifacts)
		})
	}
}

func TestFailedRoundReturnsToolErrorAndStatusLastError(t *testing.T) {
	logDestination := filepath.Join(t.TempDir(), "reviews")
	b := broker.New(broker.Options{
		LogDestination: logDestination,
		DefaultBudget:  2,
		Reviewers: []broker.ReviewerSpec{{
			Name: "failing",
			Impl: "dummy",
			Factory: func(string) reviewer.Reviewer {
				return failingReviewer{}
			},
		}},
	})
	ctx, client := newTestClientWithBroker(t, b)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
		},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sessionID := decodeStructured[OpenSessionOutput](t, openResult).SessionID

	roundResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "review_round",
		Arguments: map[string]any{
			"session_id": sessionID,
		},
	})
	errorOutput := assertToolError(t, roundResult, err, broker.CodeReviewerFailed)
	if !errorOutput.Retryable || !strings.Contains(errorOutput.NextAction, "retry") {
		t.Fatalf("error output = %+v", errorOutput)
	}

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
	if status.RoundsUsed != 0 || status.BudgetRemaining != 2 {
		t.Fatalf("status budget after failure = %+v", status)
	}
	if status.LastError == nil || status.LastError.Code != broker.CodeReviewerFailed {
		t.Fatalf("last error = %+v", status.LastError)
	}
}

func TestAsyncReviewTools(t *testing.T) {
	reviewerImpl := newBlockingReviewer(validReviewOutput())
	b := broker.New(broker.Options{
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		DefaultBudget:  2,
		MaxFindings:    config.DefaultMaxFindings,
		Reviewers: []broker.ReviewerSpec{{
			Name: "blocking",
			Impl: "dummy",
			Factory: func(string) reviewer.Reviewer {
				return reviewerImpl
			},
		}},
	})
	ctx, client := newTestClientWithBroker(t, b)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	openResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
		},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sessionID := decodeStructured[OpenSessionOutput](t, openResult).SessionID

	startResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "start_review_round",
		Arguments: map[string]any{
			"session_id": sessionID,
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

	statusResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "round_status",
		Arguments: map[string]any{
			"session_id":   sessionID,
			"round_number": 1,
		},
	})
	if err != nil {
		t.Fatalf("round status: %v", err)
	}
	status := decodeStructured[RoundStatusToolOutput](t, statusResult)
	if status.Round.State != "running" || status.Round.StatusPath == "" {
		t.Fatalf("round status output = %+v", status)
	}

	collectResult, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "collect_round",
		Arguments: map[string]any{
			"session_id":   sessionID,
			"round_number": 1,
		},
	})
	assertToolError(t, collectResult, err, broker.CodeRoundInProgress)

	reviewerImpl.release()
	var collected ReviewRoundOutput
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
			collected = decodeStructured[ReviewRoundOutput](t, collectResult)
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

func newTestClient(t *testing.T, cfg *config.Config) (context.Context, *mcp.ClientSession) {
	t.Helper()

	server, _, err := New(cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return newTestClientForServer(t, server)
}

func newTestClientWithBroker(t *testing.T, b *broker.Broker) (context.Context, *mcp.ClientSession) {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-project", Version: Version}, nil)
	RegisterTools(server, b)
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

func callRound(t *testing.T, ctx context.Context, client *mcp.ClientSession, sessionID string) ReviewRoundOutput {
	t.Helper()

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "review_round",
		Arguments: map[string]any{
			"session_id": sessionID,
		},
	})
	if err != nil {
		t.Fatalf("review round: %v", err)
	}
	return decodeStructured[ReviewRoundOutput](t, result)
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
	if payload.Error.Details == nil {
		t.Fatal("expected details object")
	}
	if payload.Error.NextAction == "" {
		t.Fatal("expected next action")
	}
	return payload.Error
}

func testConfig(t *testing.T, reviewers ...*config.ReviewerConfig) *config.Config {
	t.Helper()

	return &config.Config{
		Name:           "test-project",
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		DefaultBudget:  2,
		MaxFindings:    config.DefaultMaxFindings,
		Reviewers:      reviewers,
	}
}

func dummyReviewer() *config.ReviewerConfig {
	return &config.ReviewerConfig{Name: "dummy", Impl: "dummy"}
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
		ProposedDiffs: []schema.ProposedDiff{},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

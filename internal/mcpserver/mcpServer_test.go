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
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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
		"list_sessions",
		"open_session",
		"record_round_notes",
		"review_round",
		"session_status",
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
	if statusOutput.RoundsUsed != 2 || len(statusOutput.Rounds) != 2 || !statusOutput.Rounds[0].HasNotes {
		t.Fatalf("status output = %+v", statusOutput)
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

func TestBrokerErrorsBecomeJSONRPCErrors(t *testing.T) {
	ctx, client := newTestClient(t, testConfig(t, dummyReviewer()))

	_, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "session_status",
		Arguments: map[string]any{
			"session_id": "s_missing",
		},
	})
	assertRPCError(t, err, jsonrpc.CodeInvalidParams, broker.CodeUnknownSession)
}

func TestReviewerSelection(t *testing.T) {
	cfg := testConfig(t, &config.ReviewerConfig{Name: "codex", Impl: "codex"}, dummyReviewer())
	ctx, client := newTestClient(t, cfg)
	artifactPath := writeArtifact(t, "design.md", "# design\n")

	_, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
		},
	})
	assertRPCError(t, err, jsonrpc.CodeInvalidParams, broker.CodePanelModeUnsupported)

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
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

	_, err = client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
			"reviewers": []string{"codex", "dummy"},
		},
	})
	assertRPCError(t, err, jsonrpc.CodeInvalidParams, broker.CodePanelModeUnsupported)

	_, err = client.CallTool(ctx, &mcp.CallToolParams{
		Name: "open_session",
		Arguments: map[string]any{
			"artifacts": []map[string]any{{
				"name": "design.md",
				"path": artifactPath,
			}},
			"reviewers": []string{"missing"},
		},
	})
	assertRPCError(t, err, jsonrpc.CodeInvalidParams, broker.CodeUnknownReviewer)
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
			_, err := client.CallTool(ctx, &mcp.CallToolParams{
				Name: "open_session",
				Arguments: map[string]any{
					"artifacts": []map[string]any{{
						"name": name,
						"path": artifactPath,
					}},
				},
			})
			assertRPCError(t, err, jsonrpc.CodeInvalidParams, broker.CodeInvalidArtifacts)
		})
	}
}

func newTestClient(t *testing.T, cfg *config.Config) (context.Context, *mcp.ClientSession) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	server, _, err := New(cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

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

func assertRPCError(t *testing.T, err error, wantCode int64, wantStableCode string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected json-rpc error '%s'", wantStableCode)
	}
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error = %[1]T %[1]v, want jsonrpc.Error", err)
	}
	if rpcErr.Code != wantCode {
		t.Fatalf("json-rpc code = %d, want %d", rpcErr.Code, wantCode)
	}

	var payload errorData
	if err := json.Unmarshal(rpcErr.Data, &payload); err != nil {
		t.Fatalf("decode error data: %v; raw=%s", err, string(rpcErr.Data))
	}
	if payload.Code != wantStableCode {
		t.Fatalf("stable code = %s, want %s; payload=%+v", payload.Code, wantStableCode, payload)
	}
	if payload.Details == nil {
		t.Fatal("expected details object")
	}
}

func testConfig(t *testing.T, reviewers ...*config.ReviewerConfig) *config.Config {
	t.Helper()

	return &config.Config{
		Name:           "test-project",
		LogDestination: filepath.Join(t.TempDir(), "reviews"),
		DefaultBudget:  2,
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

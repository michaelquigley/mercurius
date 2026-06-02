//go:build integration

package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/reviewer/claude"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestReviewerRoundIntegration drives a full round through the broker with the real
// claude reviewer, confirming the chain tools -> broker -> reviewer -> round log
// works end to end. it is gated behind the integration build tag and skips when the
// claude binary is unavailable.
func TestReviewerRoundIntegration(t *testing.T) {
	binaryPath := os.Getenv("MERCURIUS_CLAUDE_BINARY")
	if binaryPath == "" {
		binaryPath = "claude"
	}
	resolved, err := exec.LookPath(binaryPath)
	if err != nil {
		t.Skipf("claude binary %q not found: %v", binaryPath, err)
	}

	logDir := t.TempDir()
	b := broker.New(broker.Options{
		LogDestination: logDir,
		MaxFindings:    config.DefaultMaxFindings,
		Reviewer: claude.New(claude.Options{
			BinaryPath: resolved,
			Model:      os.Getenv("MERCURIUS_CLAUDE_MODEL"),
		}),
		ReviewerInfo: broker.ReviewerInfo{Name: "claude", Impl: "claude"},
	})

	server := mcp.NewServer(&mcp.Implementation{Name: "integration", Version: Version}, nil)
	RegisterTools(server, b, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	artifactPath := writeArtifact(t, "design.md", "# Design\n\nA trivially complete artifact for an end-to-end smoke test.\n")

	openResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "open_session", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sessionID := decodeStructured[OpenSessionOutput](t, openResult).SessionID

	startResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "start_review_round",
		Arguments: map[string]any{
			"session_id": sessionID,
			"artifacts":  []map[string]any{{"name": "design.md", "path": artifactPath}},
		},
	})
	if err != nil {
		t.Fatalf("start review round: %v", err)
	}
	if startResult.IsError {
		t.Fatalf("start review round tool error: %+v", startResult.StructuredContent)
	}
	roundNumber := decodeStructured[StartReviewRoundOutput](t, startResult).RoundNumber

	// poll collect_round until the background round completes.
	deadline := time.After(150 * time.Second)
	var collected CollectedRoundOutput
	for {
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
			Name:      "collect_round",
			Arguments: map[string]any{"session_id": sessionID, "round_number": roundNumber},
		})
		if err != nil {
			t.Fatalf("collect round: %v", err)
		}
		if !result.IsError {
			collected = decodeStructured[CollectedRoundOutput](t, result)
			break
		}
		payload := decodeStructured[ToolErrorOutput](t, result)
		if payload.Error.Code != broker.CodeConflict {
			t.Fatalf("collect round tool error: %+v", payload.Error)
		}
		select {
		case <-deadline:
			t.Fatal("round did not complete within deadline")
		case <-time.After(500 * time.Millisecond):
		}
	}

	if collected.LogPath == "" {
		t.Fatal("expected a round log path")
	}
	if _, err := os.Stat(collected.LogPath); err != nil {
		t.Fatalf("round log not written: %v", err)
	}
	if len(collected.Reviewers) == 0 {
		t.Fatal("expected reviewer output in collected round")
	}
}

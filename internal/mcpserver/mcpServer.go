package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/reviewer/codex"
	"github.com/michaelquigley/mercurius/internal/reviewer/dummy"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "v0.0.0-dev"

type EmptyInput struct{}

type ArtifactInput struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

type OpenSessionInput struct {
	Artifacts []ArtifactInput `json:"artifacts,omitempty"`
	Reviewers []string        `json:"reviewers,omitempty"`
	Budget    int             `json:"budget,omitempty"`
}

type OpenSessionOutput struct {
	SessionID string `json:"session_id"`
	OpenedAt  string `json:"opened_at"`
	Budget    int    `json:"budget"`
}

type ReviewRoundInput struct {
	SessionID string          `json:"session_id,omitempty"`
	Artifacts []ArtifactInput `json:"artifacts,omitempty"`
}

type ReviewRoundOutput struct {
	RoundNumber int                   `json:"round_number"`
	LogPath     string                `json:"log_path"`
	Manifest    []ManifestEntryOutput `json:"manifest"`
	Reviewers   []ReviewerOutput      `json:"reviewers"`
}

type ManifestEntryOutput struct {
	Name         string `json:"name"`
	SourcePath   string `json:"source_path"`
	SnapshotPath string `json:"snapshot_path"`
	Size         int64  `json:"size"`
	Hash         string `json:"hash"`
}

type ReviewerOutput struct {
	ReviewerName string          `json:"reviewer_name"`
	Raw          json.RawMessage `json:"raw"`
	UsageNotes   string          `json:"usage_notes"`
}

type RecordRoundNotesInput struct {
	SessionID   string          `json:"session_id,omitempty"`
	RoundNumber int             `json:"round_number,omitempty"`
	Commentary  string          `json:"commentary,omitempty"`
	Decisions   []DecisionInput `json:"decisions,omitempty"`
}

type DecisionInput struct {
	Ref         string `json:"ref,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	Note        string `json:"note,omitempty"`
}

type RecordRoundNotesOutput struct {
	RoundNumber        int    `json:"round_number"`
	LogPath            string `json:"log_path"`
	CommentaryRecorded bool   `json:"commentary_recorded"`
	DecisionsRecorded  int    `json:"decisions_recorded"`
}

type CloseSessionInput struct {
	SessionID string `json:"session_id,omitempty"`
	Verdict   string `json:"verdict,omitempty"`
}

type CloseSessionOutput struct {
	SessionID string `json:"session_id"`
	Verdict   string `json:"verdict"`
	ClosedAt  string `json:"closed_at"`
}

type SessionStatusInput struct {
	SessionID string `json:"session_id,omitempty"`
}

type SessionStatusOutput struct {
	SessionID  string              `json:"session_id"`
	State      string              `json:"state"`
	Verdict    *string             `json:"verdict"`
	OpenedAt   string              `json:"opened_at"`
	ClosedAt   *string             `json:"closed_at"`
	Budget     int                 `json:"budget"`
	RoundsUsed int                 `json:"rounds_used"`
	Rounds     []RoundStatusOutput `json:"rounds"`
}

type RoundStatusOutput struct {
	RoundNumber   int    `json:"round_number"`
	OpenedAt      string `json:"opened_at"`
	LogPath       string `json:"log_path"`
	HasNotes      bool   `json:"has_notes"`
	DecisionCount int    `json:"decision_count"`
}

type ListSessionsOutput struct {
	Sessions []SessionSummaryOutput `json:"sessions"`
}

type SessionSummaryOutput struct {
	SessionID  string  `json:"session_id"`
	State      string  `json:"state"`
	Verdict    *string `json:"verdict"`
	OpenedAt   string  `json:"opened_at"`
	RoundsUsed int     `json:"rounds_used"`
}

type errorData struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// New creates a configured MCP server and its backing broker.
func New(cfg *config.Config) (*mcp.Server, *broker.Broker, error) {
	if cfg == nil {
		return nil, nil, errors.New("config is nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}

	options, err := BrokerOptions(cfg)
	if err != nil {
		return nil, nil, err
	}
	b := broker.New(options)
	server := mcp.NewServer(&mcp.Implementation{Name: cfg.Name, Version: Version}, nil)
	RegisterTools(server, b)
	return server, b, nil
}

// BrokerOptions turns project config into broker reviewer factories.
func BrokerOptions(cfg *config.Config) (broker.Options, error) {
	if cfg == nil {
		return broker.Options{}, errors.New("config is nil")
	}

	options := broker.Options{
		LogDestination:  cfg.LogDestination,
		DefaultBudget:   cfg.DefaultBudget,
		PromptOverrides: cfg.PromptOverrides,
		Reviewers:       make([]broker.ReviewerSpec, 0, len(cfg.Reviewers)),
	}

	for _, reviewerConfig := range cfg.Reviewers {
		if reviewerConfig == nil {
			return broker.Options{}, errors.New("reviewer entry is nil")
		}
		spec, err := reviewerSpec(reviewerConfig)
		if err != nil {
			return broker.Options{}, err
		}
		options.Reviewers = append(options.Reviewers, spec)
	}
	return options, nil
}

// RegisterTools installs the Mercurius MCP tool surface.
func RegisterTools(server *mcp.Server, b *broker.Broker) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "open_session",
		Description: "start a new Mercurius review session",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input OpenSessionInput) (*mcp.CallToolResult, any, error) {
		response, err := b.OpenSession(ctx, broker.OpenSessionRequest{
			Artifacts: artifactsFromInput(input.Artifacts),
			Reviewers: append([]string(nil), input.Reviewers...),
			Budget:    input.Budget,
		})
		if err != nil {
			return nil, nil, rpcError(err)
		}
		return nil, OpenSessionOutput{
			SessionID: response.SessionID,
			OpenedAt:  formatTime(response.OpenedAt),
			Budget:    response.Budget,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "review_round",
		Description: "run a review round for an active Mercurius session",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ReviewRoundInput) (*mcp.CallToolResult, any, error) {
		var artifacts []broker.Artifact
		if input.Artifacts != nil {
			artifacts = artifactsFromInput(input.Artifacts)
		}
		response, err := b.ReviewRound(ctx, broker.ReviewRoundRequest{
			SessionID: input.SessionID,
			Artifacts: artifacts,
		})
		if err != nil {
			return nil, nil, rpcError(err)
		}
		return nil, ReviewRoundOutput{
			RoundNumber: response.RoundNumber,
			LogPath:     response.LogPath,
			Manifest:    manifestOutput(response.Manifest),
			Reviewers:   reviewerOutput(response.Reviewers),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "record_round_notes",
		Description: "record commentary and human decisions for a completed round",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input RecordRoundNotesInput) (*mcp.CallToolResult, any, error) {
		response, err := b.RecordRoundNotes(ctx, broker.RecordRoundNotesRequest{
			SessionID:   input.SessionID,
			RoundNumber: input.RoundNumber,
			Commentary:  input.Commentary,
			Decisions:   decisionsFromInput(input.Decisions),
		})
		if err != nil {
			return nil, nil, rpcError(err)
		}
		return nil, RecordRoundNotesOutput{
			RoundNumber:        response.RoundNumber,
			LogPath:            response.LogPath,
			CommentaryRecorded: response.CommentaryRecorded,
			DecisionsRecorded:  response.DecisionsRecorded,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "close_session",
		Description: "close a Mercurius review session",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input CloseSessionInput) (*mcp.CallToolResult, any, error) {
		response, err := b.CloseSession(ctx, broker.CloseSessionRequest{
			SessionID: input.SessionID,
			Verdict:   input.Verdict,
		})
		if err != nil {
			return nil, nil, rpcError(err)
		}
		return nil, CloseSessionOutput{
			SessionID: response.SessionID,
			Verdict:   response.Verdict,
			ClosedAt:  formatTime(response.ClosedAt),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_status",
		Description: "return the current status of a Mercurius review session",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SessionStatusInput) (*mcp.CallToolResult, any, error) {
		response, err := b.SessionStatus(ctx, input.SessionID)
		if err != nil {
			return nil, nil, rpcError(err)
		}
		return nil, sessionStatusOutput(response), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_sessions",
		Description: "list Mercurius sessions known to this server process",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, any, error) {
		response, err := b.ListSessions(ctx)
		if err != nil {
			return nil, nil, rpcError(err)
		}
		return nil, listSessionsOutput(response), nil
	})
}

func reviewerSpec(cfg *config.ReviewerConfig) (broker.ReviewerSpec, error) {
	switch cfg.Impl {
	case "codex":
		binaryPath := cfg.BinaryPath
		model := cfg.Model
		extraArgs := append([]string(nil), cfg.ExtraArgs...)
		return broker.ReviewerSpec{
			Name: cfg.Name,
			Factory: func(sessionDir string) reviewer.Reviewer {
				return codex.New(codex.Options{
					BinaryPath: binaryPath,
					WorkingDir: sessionDir,
					Model:      model,
					ExtraArgs:  append([]string(nil), extraArgs...),
				})
			},
		}, nil
	case "dummy":
		return broker.ReviewerSpec{
			Name: cfg.Name,
			Factory: func(string) reviewer.Reviewer {
				return dummy.New()
			},
		}, nil
	default:
		return broker.ReviewerSpec{}, fmt.Errorf("reviewer '%s': unknown impl '%s'", cfg.Name, cfg.Impl)
	}
}

func rpcError(err error) error {
	var brokerErr *broker.Error
	if errors.As(err, &brokerErr) {
		var code int64 = jsonrpc.CodeInvalidParams
		if brokerErr.Code == broker.CodeInternalError {
			code = jsonrpc.CodeInternalError
		}
		return wireError(code, brokerErr.Code, brokerErr.Message, brokerErr.Details, brokerErr.Err)
	}
	return wireError(jsonrpc.CodeInternalError, broker.CodeInternalError, "internal error", nil, err)
}

func wireError(code int64, stableCode string, message string, details map[string]any, cause error) *jsonrpc.Error {
	payloadDetails := map[string]any{}
	for key, value := range details {
		payloadDetails[key] = value
	}
	if cause != nil {
		payloadDetails["cause"] = cause.Error()
	}

	raw, err := json.Marshal(errorData{
		Code:    stableCode,
		Message: message,
		Details: payloadDetails,
	})
	if err != nil {
		raw = json.RawMessage(`{"code":"internal_error","message":"internal error","details":{"cause":"error payload marshal failed"}}`)
	}
	return &jsonrpc.Error{
		Code:    code,
		Message: message,
		Data:    raw,
	}
}

func artifactsFromInput(inputs []ArtifactInput) []broker.Artifact {
	artifacts := make([]broker.Artifact, 0, len(inputs))
	for _, input := range inputs {
		artifacts = append(artifacts, broker.Artifact{
			Name: input.Name,
			Path: input.Path,
		})
	}
	return artifacts
}

func decisionsFromInput(inputs []DecisionInput) []broker.Decision {
	decisions := make([]broker.Decision, 0, len(inputs))
	for _, input := range inputs {
		decisions = append(decisions, broker.Decision{
			Ref:         input.Ref,
			Disposition: input.Disposition,
			Note:        input.Note,
		})
	}
	return decisions
}

func manifestOutput(manifest []broker.ArtifactManifestEntry) []ManifestEntryOutput {
	out := make([]ManifestEntryOutput, 0, len(manifest))
	for _, artifact := range manifest {
		out = append(out, ManifestEntryOutput{
			Name:         artifact.Name,
			SourcePath:   artifact.SourcePath,
			SnapshotPath: artifact.SnapshotPath,
			Size:         artifact.Size,
			Hash:         artifact.Hash,
		})
	}
	return out
}

func reviewerOutput(results []broker.ReviewerResult) []ReviewerOutput {
	out := make([]ReviewerOutput, 0, len(results))
	for _, result := range results {
		out = append(out, ReviewerOutput{
			ReviewerName: result.ReviewerName,
			Raw:          append(json.RawMessage(nil), result.Raw...),
			UsageNotes:   result.UsageNotes,
		})
	}
	return out
}

func sessionStatusOutput(status broker.SessionStatusResponse) SessionStatusOutput {
	return SessionStatusOutput{
		SessionID:  status.SessionID,
		State:      status.State,
		Verdict:    cloneString(status.Verdict),
		OpenedAt:   formatTime(status.OpenedAt),
		ClosedAt:   formatTimePtr(status.ClosedAt),
		Budget:     status.Budget,
		RoundsUsed: status.RoundsUsed,
		Rounds:     roundStatusOutput(status.Rounds),
	}
}

func roundStatusOutput(rounds []broker.RoundStatus) []RoundStatusOutput {
	out := make([]RoundStatusOutput, 0, len(rounds))
	for _, round := range rounds {
		out = append(out, RoundStatusOutput{
			RoundNumber:   round.RoundNumber,
			OpenedAt:      formatTime(round.OpenedAt),
			LogPath:       round.LogPath,
			HasNotes:      round.HasNotes,
			DecisionCount: round.DecisionCount,
		})
	}
	return out
}

func listSessionsOutput(response broker.ListSessionsResponse) ListSessionsOutput {
	sessions := make([]SessionSummaryOutput, 0, len(response.Sessions))
	for _, session := range response.Sessions {
		sessions = append(sessions, SessionSummaryOutput{
			SessionID:  session.SessionID,
			State:      session.State,
			Verdict:    cloneString(session.Verdict),
			OpenedAt:   formatTime(session.OpenedAt),
			RoundsUsed: session.RoundsUsed,
		})
	}
	return ListSessionsOutput{Sessions: sessions}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := formatTime(*t)
	return &formatted
}

func cloneString(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

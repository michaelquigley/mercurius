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
	"github.com/michaelquigley/mercurius/internal/schema"
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
	Reviewers []string `json:"reviewers,omitempty"`
}

type OpenSessionOutput struct {
	SessionID            string               `json:"session_id"`
	OpenedAt             string               `json:"opened_at"`
	MaxFindings          int                  `json:"max_findings"`
	ReviewContextPresent bool                 `json:"review_context_present"`
	ReviewFocusPresent   bool                 `json:"review_focus_present"`
	Reviewers            []ReviewerInfoOutput `json:"reviewers"`
}

type StartRoundInput struct {
	SessionID string          `json:"session_id,omitempty"`
	Artifacts []ArtifactInput `json:"artifacts,omitempty"`
}

type CollectedRoundOutput struct {
	RoundNumber int                   `json:"round_number"`
	LogPath     string                `json:"log_path"`
	Manifest    []ManifestEntryOutput `json:"manifest"`
	Reviewers   []ReviewerOutput      `json:"reviewers"`
	Triage      RoundTriageOutput     `json:"triage"`
	NextAction  string                `json:"next_action"`
}

type RoundTriageOutput struct {
	Mode              string                 `json:"mode"`
	TotalFindings     int                    `json:"total_findings"`
	RemainingFindings int                    `json:"remaining_findings"`
	Findings          []TriageFindingOutput  `json:"findings"`
	AdvisoryNotes    []TriageAdvisoryOutput `json:"advisory_notes"`
	NextFinding       *TriageFindingOutput   `json:"next_finding"`
	Guidance          string                 `json:"guidance"`
}

type TriageFindingOutput struct {
	ReviewerName string  `json:"reviewer_name"`
	Ref          string  `json:"ref"`
	Kind         string  `json:"kind"`
	Severity     *string `json:"severity,omitempty"`
	Location     *string `json:"location,omitempty"`
	Title        string  `json:"title"`
	Detail       string  `json:"detail"`
	Suggestion   *string `json:"suggestion,omitempty"`
}

type TriageAdvisoryOutput struct {
	ReviewerName string  `json:"reviewer_name"`
	Ref          string  `json:"ref"`
	Location     string  `json:"location"`
	Note         string  `json:"note"`
	Rationale    string  `json:"rationale"`
	Suggestion   *string `json:"suggestion,omitempty"`
}

type StartReviewRoundOutput struct {
	SessionID      string `json:"session_id"`
	RoundNumber    int    `json:"round_number"`
	State          string `json:"state"`
	Reviewer       string `json:"reviewer"`
	StartedAt      string `json:"started_at"`
	StatusPath     string `json:"status_path"`
	EventsPath     string `json:"events_path"`
	MonitorCommand string `json:"monitor_command"`
	NextAction     string `json:"next_action"`
}

type RoundStatusInput struct {
	SessionID   string `json:"session_id,omitempty"`
	RoundNumber int    `json:"round_number,omitempty"`
}

type RoundJobOutput struct {
	SessionID   string       `json:"session_id"`
	RoundNumber int          `json:"round_number"`
	State       string       `json:"state"`
	Reviewer    string       `json:"reviewer"`
	StartedAt   string       `json:"started_at"`
	UpdatedAt   string       `json:"updated_at"`
	CompletedAt *string      `json:"completed_at"`
	LogPath     string       `json:"log_path,omitempty"`
	StatusPath  string       `json:"status_path"`
	EventsPath  string       `json:"events_path"`
	Error       *ErrorOutput `json:"error"`
}

type RoundStatusToolOutput struct {
	Round RoundJobOutput `json:"round"`
}

type CollectRoundInput struct {
	SessionID   string `json:"session_id,omitempty"`
	RoundNumber int    `json:"round_number,omitempty"`
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
	NextAction         string `json:"next_action"`
}

type CloseSessionInput struct {
	SessionID string `json:"session_id,omitempty"`
}

type CloseSessionOutput struct {
	SessionID string `json:"session_id"`
	ClosedAt  string `json:"closed_at"`
}

type SessionStatusInput struct {
	SessionID string `json:"session_id,omitempty"`
}

type SessionStatusOutput struct {
	SessionID            string              `json:"session_id"`
	State                string              `json:"state"`
	OpenedAt             string              `json:"opened_at"`
	ClosedAt             *string             `json:"closed_at"`
	MaxFindings          int                 `json:"max_findings"`
	ReviewContextPresent bool                `json:"review_context_present"`
	ReviewFocusPresent   bool                `json:"review_focus_present"`
	RoundCount           int                 `json:"round_count"`
	Reviewers            []ReviewerInfoOutput `json:"reviewers"`
	LastError            *ErrorOutput        `json:"last_error"`
	ActiveRound          *RoundJobOutput     `json:"active_round"`
	LastRoundJob         *RoundJobOutput     `json:"last_round_job"`
	Rounds               []RoundStatusOutput `json:"rounds"`
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
	SessionID  string `json:"session_id"`
	State      string `json:"state"`
	OpenedAt   string `json:"opened_at"`
	RoundCount int    `json:"round_count"`
}

type ListReviewersOutput struct {
	Reviewers []ReviewerInfoOutput `json:"reviewers"`
}

type ReviewerInfoOutput struct {
	Name       string `json:"name"`
	Impl       string `json:"impl"`
	Model      string `json:"model,omitempty"`
	Selectable bool   `json:"selectable,omitempty"`
}

type ToolErrorOutput struct {
	Error ErrorOutput `json:"error"`
}

type ErrorOutput struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details"`
	Retryable  bool           `json:"retryable"`
	NextAction string         `json:"next_action"`
	At         string         `json:"at,omitempty"`
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
		LogDestination: cfg.LogDestination,
		ConfigPath:     cfg.ConfigPath,
		MaxFindings:    cfg.MaxFindings,
		ReviewContext:  cfg.ReviewContext,
		ReviewFocus:    cfg.ReviewFocus,
		Reviewers:      make([]broker.ReviewerSpec, 0, len(cfg.Reviewers)),
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
		Description: "open a new Mercurius review session. review_context and review_focus are read from mercurius.yaml; edit the YAML before opening a session if you want different review calibration. when the config has multiple reviewers, pass the chosen name in reviewers. artifacts are not registered at session open; pass them to each start_round call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input OpenSessionInput) (*mcp.CallToolResult, any, error) {
		response, err := b.OpenSession(ctx, broker.OpenSessionRequest{
			Reviewers: append([]string(nil), input.Reviewers...),
		})
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, OpenSessionOutput{
			SessionID:            response.SessionID,
			OpenedAt:             formatTime(response.OpenedAt),
			MaxFindings:          response.MaxFindings,
			ReviewContextPresent: response.ReviewContextPresent,
			ReviewFocusPresent:   response.ReviewFocusPresent,
			Reviewers:            reviewerInfoOutput(response.Reviewers, false),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "start_review_round",
		Description: "start one review round in the background and return immediately. artifacts are required and are scoped to this round only; nothing carries over between rounds in the same session. use the returned monitor_command to tell the user how to watch progress; the round may outlive the MCP client timeout.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input StartRoundInput) (*mcp.CallToolResult, any, error) {
		response, err := b.StartReviewRound(ctx, broker.StartRoundRequest{
			SessionID: input.SessionID,
			Artifacts: artifactsFromInput(input.Artifacts),
		})
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, StartReviewRoundOutput{
			SessionID:      response.SessionID,
			RoundNumber:    response.RoundNumber,
			State:          response.State,
			Reviewer:       response.Reviewer,
			StartedAt:      formatTime(response.StartedAt),
			StatusPath:     response.StatusPath,
			EventsPath:     response.EventsPath,
			MonitorCommand: response.MonitorCommand,
			NextAction:     response.NextAction,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "round_status",
		Description: "return status for a running or completed Mercurius review round. omit round_number to inspect the active or latest round.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input RoundStatusInput) (*mcp.CallToolResult, any, error) {
		response, err := b.RoundStatus(ctx, broker.RoundStatusRequest{
			SessionID:   input.SessionID,
			RoundNumber: input.RoundNumber,
		})
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, RoundStatusToolOutput{Round: roundJobOutput(response)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "collect_round",
		Description: "return a completed review round result with triage guidance. if the round is still running, pause and tell the user to keep monitoring instead of retrying immediately. when findings are present, walk them one at a time, explaining each finding and its proposed solution clearly and briefly.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input CollectRoundInput) (*mcp.CallToolResult, any, error) {
		response, err := b.CollectRound(ctx, broker.CollectRoundRequest{
			SessionID:   input.SessionID,
			RoundNumber: input.RoundNumber,
		})
		if err != nil {
			return toolErrorResult(err)
		}
		triage := triageOutput(response.Reviewers)
		return nil, CollectedRoundOutput{
			RoundNumber: response.RoundNumber,
			LogPath:     response.LogPath,
			Manifest:    manifestOutput(response.Manifest),
			Reviewers:   reviewerOutput(response.Reviewers),
			Triage:      triage,
			NextAction:  collectedRoundNextAction(triage),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "record_round_notes",
		Description: "record commentary and human decisions for a completed round. this implicitly finalizes the round; there is no separate close_round step. after this returns, pause and ask the user whether to start another round, open a new session, or stop; do not immediately call another Mercurius tool unless the user explicitly asks you to continue.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input RecordRoundNotesInput) (*mcp.CallToolResult, any, error) {
		response, err := b.RecordRoundNotes(ctx, broker.RecordRoundNotesRequest{
			SessionID:   input.SessionID,
			RoundNumber: input.RoundNumber,
			Commentary:  input.Commentary,
			Decisions:   decisionsFromInput(input.Decisions),
		})
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, RecordRoundNotesOutput{
			RoundNumber:        response.RoundNumber,
			LogPath:            response.LogPath,
			CommentaryRecorded: response.CommentaryRecorded,
			DecisionsRecorded:  response.DecisionsRecorded,
			NextAction:         "pause and ask the user whether to start another review round, open a new session, or stop",
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "close_session",
		Description: "close a Mercurius review session. sessions are light groupings of rounds; closure just marks the arc done.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input CloseSessionInput) (*mcp.CallToolResult, any, error) {
		response, err := b.CloseSession(ctx, broker.CloseSessionRequest{
			SessionID: input.SessionID,
		})
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, CloseSessionOutput{
			SessionID: response.SessionID,
			ClosedAt:  formatTime(response.ClosedAt),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_status",
		Description: "return the current status of a Mercurius review session",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SessionStatusInput) (*mcp.CallToolResult, any, error) {
		response, err := b.SessionStatus(ctx, input.SessionID)
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, sessionStatusOutput(response), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_sessions",
		Description: "list Mercurius sessions known to this server process",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, any, error) {
		response, err := b.ListSessions(ctx)
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, listSessionsOutput(response), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_reviewers",
		Description: "list reviewer names configured for this Mercurius server. use these names in open_session.reviewers when the config has multiple reviewers.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, any, error) {
		response, err := b.ListReviewers(ctx)
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, ListReviewersOutput{
			Reviewers: reviewerInfoOutput(response.Reviewers, true),
		}, nil
	})
}

func reviewerSpec(cfg *config.ReviewerConfig) (broker.ReviewerSpec, error) {
	switch cfg.Impl {
	case "codex":
		binaryPath := cfg.BinaryPath
		model := cfg.Model
		extraArgs := append([]string(nil), cfg.ExtraArgs...)
		return broker.ReviewerSpec{
			Name:  cfg.Name,
			Impl:  cfg.Impl,
			Model: cfg.Model,
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
			Name:  cfg.Name,
			Impl:  cfg.Impl,
			Model: cfg.Model,
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

func toolErrorResult(err error) (*mcp.CallToolResult, any, error) {
	var brokerErr *broker.Error
	if !errors.As(err, &brokerErr) {
		return nil, nil, rpcError(err)
	}

	output := errorOutput(broker.ErrorInfoFrom(err))
	text := fmt.Sprintf("%s: %s\nnext_action: %s", output.Code, output.Message, output.NextAction)
	if cause, ok := output.Details["cause"].(string); ok && cause != "" {
		text += "\ncause: " + cause
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
		StructuredContent: ToolErrorOutput{Error: output},
		IsError:           true,
	}, nil, nil
}

func wireError(code int64, stableCode string, message string, details map[string]any, cause error) *jsonrpc.Error {
	payloadDetails := map[string]any{}
	for key, value := range details {
		payloadDetails[key] = value
	}
	if cause != nil {
		payloadDetails["cause"] = cause.Error()
	}

	raw, err := json.Marshal(ToolErrorOutput{
		Error: ErrorOutput{
			Code:       stableCode,
			Message:    message,
			Details:    payloadDetails,
			Retryable:  broker.Retryable(stableCode),
			NextAction: broker.NextAction(stableCode),
		},
	})
	if err != nil {
		raw = json.RawMessage(`{"error":{"code":"internal_error","message":"internal error","details":{"cause":"error payload marshal failed"},"retryable":false,"next_action":"inspect details and escalate if the issue is not clear"}}`)
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

func triageOutput(results []broker.ReviewerResult) RoundTriageOutput {
	triage := RoundTriageOutput{
		Mode:     "walk_findings_one_at_a_time",
		Guidance: noFindingsTriageGuidance(),
	}
	for _, result := range results {
		var raw schema.ReviewOutput
		if err := json.Unmarshal(result.Raw, &raw); err != nil {
			continue
		}
		for _, concern := range raw.Concerns {
			severity := concern.Severity
			location := concern.Location
			triage.Findings = append(triage.Findings, TriageFindingOutput{
				ReviewerName: result.ReviewerName,
				Ref:          concern.ID,
				Kind:         "concern",
				Severity:     &severity,
				Location:     &location,
				Title:        concern.Claim,
				Detail:       concern.Rationale,
				Suggestion:   cloneString(concern.Suggestion),
			})
		}
		for _, question := range raw.Questions {
			triage.Findings = append(triage.Findings, TriageFindingOutput{
				ReviewerName: result.ReviewerName,
				Ref:          question.ID,
				Kind:         "question",
				Title:        question.Topic,
				Detail:       question.WhyItBlocks,
			})
		}
		for _, note := range raw.AdvisoryNotes {
			triage.AdvisoryNotes = append(triage.AdvisoryNotes, TriageAdvisoryOutput{
				ReviewerName: result.ReviewerName,
				Ref:          note.ID,
				Location:     note.Location,
				Note:         note.Note,
				Rationale:    note.Rationale,
				Suggestion:   cloneString(note.Suggestion),
			})
		}
	}
	triage.TotalFindings = len(triage.Findings)
	if triage.TotalFindings > 0 {
		// triage.findings preserves the reviewer's emitted order; only
		// next_finding is sorted by severity. The design agent can re-sort
		// findings itself if needed.
		next := selectNextFinding(triage.Findings)
		triage.NextFinding = &next
		triage.RemainingFindings = triage.TotalFindings - 1
		triage.Guidance = oneFindingTriageGuidance()
	}
	return triage
}

// selectNextFinding picks the highest-severity concern first
// (blocker > major > minor), ties broken by lexicographic id order.
// Questions follow concerns. Severity is only meaningful on concerns;
// questions sort among themselves by id alone.
func selectNextFinding(findings []TriageFindingOutput) TriageFindingOutput {
	best := -1
	for i, f := range findings {
		if best < 0 {
			best = i
			continue
		}
		if compareTriageFindings(f, findings[best]) < 0 {
			best = i
		}
	}
	return findings[best]
}

func compareTriageFindings(a, b TriageFindingOutput) int {
	// concerns rank ahead of questions.
	aIsConcern := a.Kind == "concern"
	bIsConcern := b.Kind == "concern"
	if aIsConcern != bIsConcern {
		if aIsConcern {
			return -1
		}
		return 1
	}
	// among concerns, severity rank decides; lower rank value sorts first.
	if aIsConcern {
		ra := severityRank(a.Severity)
		rb := severityRank(b.Severity)
		if ra != rb {
			return ra - rb
		}
	}
	// final tiebreak is lexicographic id order.
	if a.Ref < b.Ref {
		return -1
	}
	if a.Ref > b.Ref {
		return 1
	}
	return 0
}

func severityRank(severity *string) int {
	if severity == nil {
		return 99
	}
	switch *severity {
	case "blocker":
		return 0
	case "major":
		return 1
	case "minor":
		return 2
	default:
		return 99
	}
}

func oneFindingTriageGuidance() string {
	return "First present all entries in triage.findings as a concise overview so the user sees the full review landscape. Present triage.advisory_notes separately as non-blocking polish when present. Then walk findings one at a time, defaulting to triage.next_finding unless the user chooses another ref. For each finding: explain the finding and its proposed solution clearly and simply, using few words; discuss it with the user; implement the fix in the artifacts once you and the user are aligned. Then stop and wait - do not advance to the next finding, record notes, or call another Mercurius tool until the user explicitly responds. This preserves a fresh turn and tool-call budget for each finding."
}

func noFindingsTriageGuidance() string {
	return "No concerns or questions were returned. Summarize that there are no blocking findings, present triage.advisory_notes separately if present, then ask whether to record notes, start another review round, or close the session."
}

func collectedRoundNextAction(triage RoundTriageOutput) string {
	if triage.NextFinding != nil {
		return "pause; present all triage.findings as a concise overview with triage.advisory_notes separate if present; then walk findings one at a time starting with triage.next_finding, explaining each finding and its proposed solution clearly and briefly in few words, discussing with the user, and implementing the fix once aligned; stop and wait between findings"
	}
	return "pause and tell the user this round returned no blocking findings, with triage.advisory_notes separate if present; ask whether to record notes, start another review round, or close the session"
}

func sessionStatusOutput(status broker.SessionStatusResponse) SessionStatusOutput {
	return SessionStatusOutput{
		SessionID:            status.SessionID,
		State:                status.State,
		OpenedAt:             formatTime(status.OpenedAt),
		ClosedAt:             formatTimePtr(status.ClosedAt),
		MaxFindings:          status.MaxFindings,
		ReviewContextPresent: status.ReviewContextPresent,
		ReviewFocusPresent:   status.ReviewFocusPresent,
		RoundCount:           status.RoundCount,
		Reviewers:            reviewerInfoOutput(status.Reviewers, false),
		LastError:            errorOutputPtr(status.LastError),
		ActiveRound:          roundJobOutputPtr(status.ActiveRound),
		LastRoundJob:         roundJobOutputPtr(status.LastRoundJob),
		Rounds:               roundStatusOutput(status.Rounds),
	}
}

func roundJobOutputPtr(status *broker.RoundStatusResponse) *RoundJobOutput {
	if status == nil {
		return nil
	}
	out := roundJobOutput(*status)
	return &out
}

func roundJobOutput(status broker.RoundStatusResponse) RoundJobOutput {
	return RoundJobOutput{
		SessionID:   status.SessionID,
		RoundNumber: status.RoundNumber,
		State:       status.State,
		Reviewer:    status.Reviewer,
		StartedAt:   formatTime(status.StartedAt),
		UpdatedAt:   formatTime(status.UpdatedAt),
		CompletedAt: formatTimePtr(status.CompletedAt),
		LogPath:     status.LogPath,
		StatusPath:  status.StatusPath,
		EventsPath:  status.EventsPath,
		Error:       errorOutputPtr(status.Error),
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
			OpenedAt:   formatTime(session.OpenedAt),
			RoundCount: session.RoundCount,
		})
	}
	return ListSessionsOutput{Sessions: sessions}
}

func reviewerInfoOutput(reviewers []broker.ReviewerInfo, selectable bool) []ReviewerInfoOutput {
	out := make([]ReviewerInfoOutput, 0, len(reviewers))
	for _, reviewer := range reviewers {
		out = append(out, ReviewerInfoOutput{
			Name:       reviewer.Name,
			Impl:       reviewer.Impl,
			Model:      reviewer.Model,
			Selectable: selectable,
		})
	}
	return out
}

func errorOutputPtr(info *broker.ErrorInfo) *ErrorOutput {
	if info == nil {
		return nil
	}
	out := errorOutput(info)
	return &out
}

func errorOutput(info *broker.ErrorInfo) ErrorOutput {
	if info == nil {
		return ErrorOutput{
			Code:       broker.CodeInternalError,
			Message:    "internal error",
			Details:    map[string]any{},
			Retryable:  broker.Retryable(broker.CodeInternalError),
			NextAction: broker.NextAction(broker.CodeInternalError),
		}
	}
	return ErrorOutput{
		Code:       info.Code,
		Message:    info.Message,
		Details:    cloneDetails(info.Details),
		Retryable:  info.Retryable,
		NextAction: info.NextAction,
		At:         formatTimeOptional(info.At),
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func formatTimeOptional(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return formatTime(t)
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

func cloneDetails(details map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range details {
		out[key] = value
	}
	return out
}

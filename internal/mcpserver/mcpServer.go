package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/reviewer/claude"
	"github.com/michaelquigley/mercurius/internal/reviewer/codex"
	"github.com/michaelquigley/mercurius/internal/reviewer/dummy"
	"github.com/michaelquigley/mercurius/internal/reviewer/pi"
	"github.com/michaelquigley/mercurius/internal/schema"
	"github.com/michaelquigley/push/build"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "v0.0.0-dev"

type EmptyInput struct{}

type ArtifactInput struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

type OpenSessionInput struct{}

type OpenSessionOutput struct {
	SessionID            string             `json:"session_id"`
	OpenedAt             string             `json:"opened_at"`
	MaxFindings          int                `json:"max_findings"`
	ReviewContextPresent bool               `json:"review_context_present"`
	ReviewFocusPresent   bool               `json:"review_focus_present"`
	Reviewer             ReviewerInfoOutput `json:"reviewer"`
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
	TotalFindings     int                    `json:"total_findings"`
	RemainingFindings int                    `json:"remaining_findings"`
	Findings          []TriageFindingOutput  `json:"findings"`
	AdvisoryNotes     []TriageAdvisoryOutput `json:"advisory_notes"`
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
	MonitorCommand string `json:"monitor_command"`
	NextAction     string `json:"next_action"`
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
	Error       *ErrorOutput `json:"error"`
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
	SessionID    string `json:"session_id"`
	ClosedAt     string `json:"closed_at"`
	SynopsisPath string `json:"synopsis_path"`
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
	Reviewer             ReviewerInfoOutput  `json:"reviewer"`
	LastError            *ErrorOutput        `json:"last_error"`
	ActiveRound          *RoundJobOutput     `json:"active_round"`
	Rounds               []RoundStatusOutput `json:"rounds"`
}

type RoundStatusOutput struct {
	RoundNumber   int    `json:"round_number"`
	OpenedAt      string `json:"opened_at"`
	LogPath       string `json:"log_path"`
	HasNotes      bool   `json:"has_notes"`
	DecisionCount int    `json:"decision_count"`
}

type ReviewerInfoOutput struct {
	Name  string `json:"name"`
	Impl  string `json:"impl"`
	Model string `json:"model,omitempty"`
}

type ToolErrorOutput struct {
	Error ErrorOutput `json:"error"`
}

type ErrorOutput struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
	At      string         `json:"at,omitempty"`
}

// CalibrationProvider returns the calibration to attach to a round: the
// review_context and review_focus prose, the settled-decisions guards, and the
// raw config bytes that produced them (snapshotted per round as _config.yaml).
// Production wiring re-reads mercurius.yaml on each call so edits between rounds
// take effect on the next round without a server restart. A nil provider means
// no calibration is attached (the round prompt's calibration sections stay
// empty and no _config.yaml is written); this is fine for tests that don't
// exercise calibration.
type CalibrationProvider func(ctx context.Context) (reviewContext, reviewFocus string, settled []broker.SettledDecision, raw []byte, err error)

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
	server := mcp.NewServer(&mcp.Implementation{Name: cfg.Name, Version: build.String()}, nil)
	RegisterTools(server, b, ConfigCalibrationProvider(cfg.ConfigPath))
	return server, b, nil
}

// BrokerOptions builds broker options from a project config. Calibration
// (review_context, review_focus, settled_decisions) is intentionally not
// included here; the MCP layer re-reads it per round via a CalibrationProvider
// so YAML edits between rounds are picked up.
func BrokerOptions(cfg *config.Config) (broker.Options, error) {
	if cfg == nil {
		return broker.Options{}, errors.New("config is nil")
	}
	if cfg.Reviewer == nil {
		return broker.Options{}, errors.New("reviewer is required")
	}

	r, info, err := buildReviewer(cfg.Reviewer)
	if err != nil {
		return broker.Options{}, err
	}

	return broker.Options{
		LogDestination: cfg.LogDestination,
		ConfigPath:     cfg.ConfigPath,
		MaxFindings:    cfg.MaxFindings,
		Reviewer:       r,
		ReviewerInfo:   info,
	}, nil
}

// ConfigCalibrationProvider returns a CalibrationProvider that re-reads
// mercurius.yaml at the given path on every call and returns its
// review_context / review_focus / settled_decisions fields plus the exact bytes
// it read. It reads the file once via config.LoadWithRaw, so the returned bytes
// are exactly what produced the parsed calibration.
func ConfigCalibrationProvider(configPath string) CalibrationProvider {
	if configPath == "" {
		return nil
	}
	return func(ctx context.Context) (string, string, []broker.SettledDecision, []byte, error) {
		if err := ctx.Err(); err != nil {
			return "", "", nil, nil, err
		}
		cfg, raw, err := config.LoadWithRaw(configPath)
		if err != nil {
			return "", "", nil, nil, err
		}
		return cfg.ReviewContext, cfg.ReviewFocus, brokerSettledDecisions(cfg.SettledDecisions), raw, nil
	}
}

// brokerSettledDecisions maps config guards into the broker's shape.
func brokerSettledDecisions(in []config.SettledDecision) []broker.SettledDecision {
	if len(in) == 0 {
		return nil
	}
	out := make([]broker.SettledDecision, 0, len(in))
	for _, d := range in {
		out = append(out, broker.SettledDecision{ID: d.ID, DoNotFlag: d.DoNotFlag})
	}
	return out
}

// recordRoundNotesDispositions are the valid disposition values, surfaced on the
// record_round_notes input schema so a client sees them at the boundary. They
// mirror broker.validDisposition, which stays as a defense-in-depth backstop.
var recordRoundNotesDispositions = []any{"fixed", "rejected", "deferred"}

// recordRoundNotesInputSchema is an explicit input schema for record_round_notes.
// reflection inference leaves disposition an unconstrained string with no
// description, so a client cannot see the valid values from the schema and often
// malforms the first call (the dropped-decisions defect). spelling the schema out
// surfaces the disposition enum and field descriptions, and because the SDK
// validates incoming arguments against the resolved schema, an invalid
// disposition is rejected at the boundary before the broker is reached.
func recordRoundNotesInputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"session_id": {
				Type:        "string",
				Description: "the session id returned by open_session.",
			},
			"round_number": {
				Type:        "integer",
				Description: "the round number whose notes are being recorded.",
			},
			"commentary": {
				Type:        "string",
				Description: "free-form human commentary for the round.",
			},
			"decisions": {
				Type:        "array",
				Description: "one entry per reviewer finding triaged this round; include the full array on the first call rather than a later one.",
				Items: &jsonschema.Schema{
					Type:     "object",
					Required: []string{"ref", "disposition"},
					Properties: map[string]*jsonschema.Schema{
						"ref": {
							Type:        "string",
							Description: "the reviewer finding id this decision is about (a concern, question, or advisory id from the round).",
						},
						"disposition": {
							Type:        "string",
							Enum:        recordRoundNotesDispositions,
							Description: "how the finding was handled: 'fixed' (addressed in the artifacts), 'rejected' (declined, e.g. out of scope), or 'deferred' (acknowledged, to be handled later).",
						},
						"note": {
							Type:        "string",
							Description: "optional human note explaining the decision.",
						},
					},
				},
			},
		},
	}
}

// RegisterTools installs the Mercurius MCP tool surface. The provider is
// consulted at the start of each round (and once at open_session for the
// at-open presence snapshot) so calibration tracks mercurius.yaml edits without
// a server restart.
func RegisterTools(server *mcp.Server, b *broker.Broker, calibration CalibrationProvider) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "open_session",
		Description: "open a new Mercurius review session, a lightweight container for rounds. calibration (review_context, review_focus, settled_decisions) is not frozen here: it is re-read from mercurius.yaml at the start of every round, so edits between rounds take effect on the next round without restarting the server. the review_context_present and review_focus_present booleans on the response are an at-open snapshot only and are not a guarantee for later rounds. artifacts are not registered at session open; pass them to each start_review_round call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ OpenSessionInput) (*mcp.CallToolResult, any, error) {
		var reviewContext, reviewFocus string
		if calibration != nil {
			rc, rf, _, _, err := calibration(ctx)
			if err != nil {
				return toolErrorResult(&broker.Error{
					Code:    broker.CodeUserError,
					Message: "reread mercurius.yaml",
					Err:     err,
				})
			}
			reviewContext = rc
			reviewFocus = rf
		}
		response, err := b.OpenSession(ctx, broker.OpenSessionRequest{
			ReviewContext: reviewContext,
			ReviewFocus:   reviewFocus,
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
			Reviewer:             reviewerInfoOutput(response.Reviewer),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "start_review_round",
		Description: "start one review round in the background and return immediately. artifacts are required and are scoped to this round only; nothing carries over between rounds in the same session. review_context, review_focus, and settled_decisions are re-read from mercurius.yaml at the start of this round, so edits between rounds take effect immediately with no session reopen. if the YAML is mid-edit or unreadable this errors with code 'user_error' and the session stays active; fix the file and start the round again. use the returned monitor_command to tell the user how to watch progress; the round may outlive the MCP client timeout.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input StartRoundInput) (*mcp.CallToolResult, any, error) {
		var reviewContext, reviewFocus string
		var settled []broker.SettledDecision
		var rawConfig []byte
		if calibration != nil {
			rc, rf, sd, raw, err := calibration(ctx)
			if err != nil {
				return toolErrorResult(&broker.Error{
					Code:    broker.CodeUserError,
					Message: "reread mercurius.yaml",
					Err:     err,
				})
			}
			reviewContext = rc
			reviewFocus = rf
			settled = sd
			rawConfig = raw
		}
		response, err := b.StartReviewRound(ctx, broker.StartRoundRequest{
			SessionID:        input.SessionID,
			Artifacts:        artifactsFromInput(input.Artifacts),
			ReviewContext:    reviewContext,
			ReviewFocus:      reviewFocus,
			SettledDecisions: settled,
			RawConfig:        rawConfig,
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
			MonitorCommand: response.MonitorCommand,
			NextAction:     response.NextAction,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "collect_round",
		Description: "return a completed review round result with triage guidance. if the round is still running, this errors with code 'conflict'; tell the user to keep monitoring instead of retrying immediately. when findings are present, walk them one at a time, explaining each finding and its proposed solution clearly and briefly.",
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
		Description: "record commentary and human decisions for a completed round. each decision is {ref, disposition, note}; disposition must be one of 'fixed', 'rejected', or 'deferred'. include the full decisions array on the FIRST call rather than recording commentary first and decisions on a later call. notes land in a sibling _notes.md file inside the round directory; there is no separate close_round step. after this returns, pause and ask the user whether to start another round, open a new session, or stop; do not immediately call another Mercurius tool unless the user explicitly asks you to continue.",
		InputSchema: recordRoundNotesInputSchema(),
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
		Description: "close a Mercurius review session. sessions are light groupings of rounds; closure marks the arc done and writes a human-readable _synopsis.md at the session root that summarizes every round, its findings, and any recorded decisions. the synopsis_path field on the response points at that file.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input CloseSessionInput) (*mcp.CallToolResult, any, error) {
		response, err := b.CloseSession(ctx, broker.CloseSessionRequest{
			SessionID: input.SessionID,
		})
		if err != nil {
			return toolErrorResult(err)
		}
		return nil, CloseSessionOutput{
			SessionID:    response.SessionID,
			ClosedAt:     formatTime(response.ClosedAt),
			SynopsisPath: response.SynopsisPath,
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
}

func buildReviewer(cfg *config.ReviewerConfig) (reviewer.Reviewer, broker.ReviewerInfo, error) {
	info := broker.ReviewerInfo{Name: cfg.Name, Impl: cfg.Impl, Model: cfg.Model}
	switch cfg.Impl {
	case "codex":
		return codex.New(codex.Options{
			BinaryPath: cfg.BinaryPath,
			Model:      cfg.Model,
			ExtraArgs:  append([]string(nil), cfg.ExtraArgs...),
		}), info, nil
	case "claude":
		return claude.New(claude.Options{
			BinaryPath: cfg.BinaryPath,
			Model:      cfg.Model,
			ExtraArgs:  append([]string(nil), cfg.ExtraArgs...),
		}), info, nil
	case "pi":
		return pi.New(pi.Options{
			BinaryPath: cfg.BinaryPath,
			Model:      cfg.Model,
			ExtraArgs:  append([]string(nil), cfg.ExtraArgs...),
		}), info, nil
	case "dummy":
		return dummy.New(), info, nil
	default:
		return nil, broker.ReviewerInfo{}, fmt.Errorf("reviewer '%s': unknown impl '%s'", cfg.Name, cfg.Impl)
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
	text := fmt.Sprintf("%s: %s", output.Code, output.Message)
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
			Code:    stableCode,
			Message: message,
			Details: payloadDetails,
		},
	})
	if err != nil {
		raw = json.RawMessage(`{"error":{"code":"internal_error","message":"internal error","details":{"cause":"error payload marshal failed"}}}`)
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
	aIsConcern := a.Kind == "concern"
	bIsConcern := b.Kind == "concern"
	if aIsConcern != bIsConcern {
		if aIsConcern {
			return -1
		}
		return 1
	}
	if aIsConcern {
		ra := severityRank(a.Severity)
		rb := severityRank(b.Severity)
		if ra != rb {
			return ra - rb
		}
	}
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
	return "First present all entries in triage.findings as a concise overview so the user sees the full review landscape. Present triage.advisory_notes separately as non-blocking polish when present. Then walk findings one at a time, defaulting to triage.next_finding unless the user chooses another ref. For each finding, compress it and its proposed solution to the plainest, fewest-words version you can - hedges stripped, jargon removed - so the user can make a fast call; this radical compression is the default, not a fallback. Present that, then stop and wait for the user's decision before acting - do not implement the fix until the user has actually responded, because confidence that you can predict their decision is not consent. Implement only after they respond. Then stop and wait again - do not advance to the next finding, record notes, or call another Mercurius tool until the user explicitly responds. One finding per turn, coming and going: this preserves a fresh turn and tool-call budget for each finding."
}

func noFindingsTriageGuidance() string {
	return "No concerns or questions were returned. Summarize that there are no blocking findings, present triage.advisory_notes separately if present, then ask whether to record notes, start another review round, or close the session."
}

func collectedRoundNextAction(triage RoundTriageOutput) string {
	if triage.NextFinding != nil {
		return "pause; present all triage.findings as a concise overview with triage.advisory_notes separate if present; then walk findings one at a time starting with triage.next_finding; for each, compress the finding and its proposed solution to the plainest, fewest-words version, present it, then stop and wait for the user's decision before acting - confidence that you can predict their decision is not consent; implement only after they respond, then stop and wait again before advancing to the next finding"
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
		Reviewer:             reviewerInfoOutput(status.Reviewer),
		LastError:            errorOutputPtr(status.LastError),
		ActiveRound:          roundJobOutputPtr(status.ActiveRound),
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

func reviewerInfoOutput(r broker.ReviewerInfo) ReviewerInfoOutput {
	return ReviewerInfoOutput{Name: r.Name, Impl: r.Impl, Model: r.Model}
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
			Code:    broker.CodeInternalError,
			Message: "internal error",
			Details: map[string]any{},
		}
	}
	return ErrorOutput{
		Code:    info.Code,
		Message: info.Message,
		Details: cloneDetails(info.Details),
		At:      formatTimeOptional(info.At),
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

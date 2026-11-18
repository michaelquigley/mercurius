package mcpserver

import (
	"context"
	"time"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/runner"
	"github.com/michaelquigley/mercurius/internal/schema"
	"github.com/michaelquigley/push/build"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "v0.0.0-dev"

type EmptyInput struct{}

type ArtifactInput struct {
	Name string `dd:",+required"`
	Path string `dd:",+required"`
}

type OpenSessionInput struct{}

// OpenSessionOutput describes a new session. max_findings, the calibration
// presence flags, and reviewer describe the configuration at the moment of the
// call, as information; they are not stored on the session. reviewer is omitted
// when no reviewer is configured.
type OpenSessionOutput struct {
	SessionID            string
	OpenedAt             time.Time
	MaxFindings          int
	ReviewContextPresent bool
	ReviewFocusPresent   bool
	Reviewer             *ReviewerInfoOutput
}

type StartRoundInput struct {
	SessionID string          `dd:",+required"`
	Artifacts []ArtifactInput `dd:",+required"`
}

type CollectedRoundOutput struct {
	RoundNumber int
	LogPath     string
	Manifest    []ManifestEntryOutput
	Reviewers   []ReviewerOutput
	Triage      RoundTriageOutput
	NextAction  string
}

type RoundTriageOutput struct {
	TotalFindings     int
	RemainingFindings int
	Findings          []TriageFindingOutput
	AdvisoryNotes     []TriageAdvisoryOutput
	NextFinding       *TriageFindingOutput
	Guidance          string
}

type TriageFindingOutput struct {
	ReviewerName string
	Ref          string
	Kind         string
	Severity     *string
	Location     *string
	Title        string
	Detail       string
	Suggestion   *string
}

type TriageAdvisoryOutput struct {
	ReviewerName string
	Ref          string
	Location     string
	Note         string
	Rationale    string
	Suggestion   *string
}

// StartReviewRoundOutput describes a round handed to its runner. status_path
// points at the round's _state.json.
type StartReviewRoundOutput struct {
	SessionID      string
	RoundNumber    int
	State          string
	Reviewer       ReviewerInfoOutput
	StartedAt      time.Time
	StatusPath     string
	MonitorCommand string
	NextAction     string
}

type CollectRoundInput struct {
	SessionID   string `dd:",+required"`
	RoundNumber int
}

type ManifestEntryOutput struct {
	Name         string
	SourcePath   string
	SnapshotPath string
	Size         int64
	Hash         string
}

type ReviewerOutput struct {
	ReviewerName string
	Raw          map[string]any `dd:",+opaque"`
	UsageNotes   string
}

type RecordRoundNotesInput struct {
	SessionID   string `dd:",+required"`
	RoundNumber int    `dd:",+required"`
	Commentary  string
	Decisions   []DecisionInput
}

type DecisionInput struct {
	Ref         string `dd:",+required"`
	Disposition string `dd:",+required"`
	Note        string
}

type RecordRoundNotesOutput struct {
	RoundNumber        int
	LogPath            string
	CommentaryRecorded bool
	DecisionsRecorded  int
	NextAction         string
}

type CloseSessionInput struct {
	SessionID string `dd:",+required"`
}

type CloseSessionOutput struct {
	SessionID    string
	ClosedAt     time.Time
	SynopsisPath string
}

type SessionStatusInput struct {
	SessionID string `dd:",+required"`
}

// SessionStatusOutput is a read-only session view. the reviewer, finding limit,
// and any failure are reported per round.
type SessionStatusOutput struct {
	SessionID  string
	State      string
	OpenedAt   time.Time
	ClosedAt   *time.Time
	RoundCount int
	Rounds     []RoundStatusOutput
}

type RoundStatusOutput struct {
	RoundNumber    int
	OpenedAt       time.Time
	State          string
	Reviewer       ReviewerInfoOutput
	MaxFindings    int
	StatePath      string
	LogPath        string `dd:",+omitempty"`
	CompletedAt    *time.Time
	HasNotes       bool
	DecisionCount  int
	OutlivedServer bool
	Error          *ErrorOutput
}

type ReviewerInfoOutput struct {
	Name       string
	Impl       string
	Model      string `dd:",+omitempty"`
	BinaryPath string `dd:",+omitempty"`
	ExtraArgs  []string
	Timeout    string
	// Source is where the reviewer came from: 'user config' or 'project
	// override'.
	Source string
}

type ToolErrorOutput struct {
	Error ErrorOutput
}

type ErrorOutput struct {
	Code    string
	Message string
	Details map[string]any
	At      time.Time `dd:",+omitempty"`
}

// Options configures a server.
type Options struct {
	// ConfigPath is the project mercurius.yaml, resolved on every tool call.
	ConfigPath string
	// UserConfigPath is the user-level config, which holds the reviewer. it is
	// read on every tool call, alongside the project file.
	UserConfigPath string
	// Launcher starts rounds. nil means the production launcher: each round in
	// its own detached mercurius process.
	Launcher runner.Launcher
}

// New creates an MCP server and its backing broker. it reads no configuration:
// every tool call resolves it, so a missing or broken mercurius.yaml is a tool
// error with a reason rather than a server that cannot start. the server's name
// is the base name of the directory holding the project config path.
func New(options Options) (*mcp.Server, *broker.Broker, error) {
	configPath, err := config.ProjectPath(options.ConfigPath)
	if err != nil {
		return nil, nil, err
	}
	launcher := options.Launcher
	if launcher == nil {
		processLauncher, err := runner.NewProcessLauncher()
		if err != nil {
			return nil, nil, err
		}
		launcher = processLauncher
	}
	b := broker.New(broker.Options{
		ConfigPath:     configPath,
		UserConfigPath: options.UserConfigPath,
		Launcher:       launcher,
	})
	server := mcp.NewServer(&mcp.Implementation{Name: config.ProjectName(configPath), Version: build.String()}, nil)
	if err := RegisterTools(server, b); err != nil {
		return nil, nil, err
	}
	return server, b, nil
}

// RegisterTools installs the Mercurius MCP tool surface. the broker resolves
// the configuration on every call, so edits to mercurius.yaml take effect on the
// next call without a server restart.
func RegisterTools(server *mcp.Server, b *broker.Broker) error {
	if err := register(server, tool[OpenSessionInput, OpenSessionOutput]{
		name:        "open_session",
		description: "open a new Mercurius review session, a lightweight container for rounds. sessions live on disk, so any later server process can serve this session by its id. if mercurius.yaml is missing or unreadable this errors with code 'user_error' naming the path; write or fix the file and call again. the reviewer is not set in mercurius.yaml: it comes from the operator's user-level config, and a 'reviewer' block in mercurius.yaml is a 'user_error' telling you to remove it; remove it, do not rename it. the max_findings, review_context_present, review_focus_present, and reviewer fields describe the configuration at this moment, as information; they are not frozen on the session. calibration, the reviewer, and max_findings are re-read at the start of every round. artifacts are not registered at session open; pass them to each start_review_round call.",
		input:       objectSchema(nil),
		run: func(ctx context.Context, _ OpenSessionInput) (OpenSessionOutput, error) {
			response, err := b.OpenSession(ctx, broker.OpenSessionRequest{})
			if err != nil {
				return OpenSessionOutput{}, err
			}
			return OpenSessionOutput{
				SessionID:            response.SessionID,
				OpenedAt:             response.OpenedAt,
				MaxFindings:          response.MaxFindings,
				ReviewContextPresent: response.ReviewContextPresent,
				ReviewFocusPresent:   response.ReviewFocusPresent,
				Reviewer:             reviewerInfoOutputPtr(response.Reviewer),
			}, nil
		},
	}); err != nil {
		return err
	}

	if err := register(server, tool[StartRoundInput, StartReviewRoundOutput]{
		name:        "start_review_round",
		description: "start one review round in the background and return immediately. artifacts are required and are scoped to this round only; nothing carries over between rounds in the same session. review_context, review_focus, settled_decisions, and max_findings are resolved from mercurius.yaml, and the reviewer from the operator's user-level config, at the start of this round and recorded on it. if either file is missing, mid-edit, or broken, or no reviewer is configured, this errors with code 'user_error' naming the file and nothing is started; fix it and start the round again. the response's reviewer.source says where the reviewer came from ('user config' or 'project override'); on a project override, tell the user the round runs on the project's own reviewer. one round runs at a time per project: if another round is running, in this session or any other, this errors with code 'conflict' naming that round; do not open a new session to get around it, tell the user and collect that round when it finishes. a failed round keeps its number, so a retry is the next round. use the returned monitor_command to tell the user how to watch progress; the round may outlive the MCP client timeout.",
		input:       startRoundInputSchema(),
		run: func(ctx context.Context, input StartRoundInput) (StartReviewRoundOutput, error) {
			response, err := b.StartReviewRound(ctx, broker.StartRoundRequest{
				SessionID: input.SessionID,
				Artifacts: artifactsFromInput(input.Artifacts),
			})
			if err != nil {
				return StartReviewRoundOutput{}, err
			}
			return StartReviewRoundOutput{
				SessionID:      response.SessionID,
				RoundNumber:    response.RoundNumber,
				State:          response.State,
				Reviewer:       reviewerInfoOutput(response.Reviewer),
				StartedAt:      response.StartedAt,
				StatusPath:     response.StatusPath,
				MonitorCommand: response.MonitorCommand,
				NextAction:     response.NextAction,
			}, nil
		},
	}); err != nil {
		return err
	}

	if err := register(server, tool[CollectRoundInput, CollectedRoundOutput]{
		name:        "collect_round",
		description: "return a completed review round result with triage guidance. if the round is still running, this errors with code 'conflict'; tell the user to keep monitoring instead of retrying immediately. if the round failed, this returns the stored failure as a tool error, for as long as the round directory exists; the reviewer's raw output, when there was any, is at details.raw_path. when findings are present, walk them one at a time, explaining each finding and its proposed solution clearly and briefly.",
		input:       collectRoundInputSchema(),
		run: func(ctx context.Context, input CollectRoundInput) (CollectedRoundOutput, error) {
			response, err := b.CollectRound(ctx, broker.CollectRoundRequest{
				SessionID:   input.SessionID,
				RoundNumber: input.RoundNumber,
			})
			if err != nil {
				return CollectedRoundOutput{}, err
			}
			triage := triageOutput(response.Reviewers)
			return CollectedRoundOutput{
				RoundNumber: response.RoundNumber,
				LogPath:     response.LogPath,
				Manifest:    manifestOutput(response.Manifest),
				Reviewers:   reviewerOutput(response.Reviewers),
				Triage:      triage,
				NextAction:  collectedRoundNextAction(triage),
			}, nil
		},
	}); err != nil {
		return err
	}

	if err := register(server, tool[RecordRoundNotesInput, RecordRoundNotesOutput]{
		name:        "record_round_notes",
		description: "record commentary and human decisions for a completed round; a failed or running round answers 'conflict'. each decision is {ref, disposition, note}; disposition must be one of 'fixed', 'rejected', or 'deferred'. include the full decisions array on the FIRST call rather than recording commentary first and decisions on a later call. notes land in _notes.json and its human twin _notes.md inside the round directory; there is no separate close_round step. after this returns, pause and ask the user whether to start another round, open a new session, or stop; do not immediately call another Mercurius tool unless the user explicitly asks you to continue.",
		input:       recordRoundNotesInputSchema(),
		run: func(ctx context.Context, input RecordRoundNotesInput) (RecordRoundNotesOutput, error) {
			response, err := b.RecordRoundNotes(ctx, broker.RecordRoundNotesRequest{
				SessionID:   input.SessionID,
				RoundNumber: input.RoundNumber,
				Commentary:  input.Commentary,
				Decisions:   decisionsFromInput(input.Decisions),
			})
			if err != nil {
				return RecordRoundNotesOutput{}, err
			}
			return RecordRoundNotesOutput{
				RoundNumber:        response.RoundNumber,
				LogPath:            response.LogPath,
				CommentaryRecorded: response.CommentaryRecorded,
				DecisionsRecorded:  response.DecisionsRecorded,
				NextAction:         "pause and ask the user whether to start another review round, open a new session, or stop",
			}, nil
		},
	}); err != nil {
		return err
	}

	if err := register(server, tool[CloseSessionInput, CloseSessionOutput]{
		name:        "close_session",
		description: "close a Mercurius review session. sessions are light groupings of rounds; closure marks the arc done and writes a human-readable _synopsis.md at the session root, rendered from disk, that summarizes every round - failed ones included - with its reviewer, findings, and any recorded decisions. the synopsis_path field on the response points at that file.",
		input:       sessionIDInputSchema(),
		run: func(ctx context.Context, input CloseSessionInput) (CloseSessionOutput, error) {
			response, err := b.CloseSession(ctx, broker.CloseSessionRequest{
				SessionID: input.SessionID,
			})
			if err != nil {
				return CloseSessionOutput{}, err
			}
			return CloseSessionOutput{
				SessionID:    response.SessionID,
				ClosedAt:     response.ClosedAt,
				SynopsisPath: response.SynopsisPath,
			}, nil
		},
	}); err != nil {
		return err
	}

	return register(server, tool[SessionStatusInput, SessionStatusOutput]{
		name:        "session_status",
		description: "return the current status of a Mercurius review session: every round with its state (running, completed, or failed), reviewer, finding limit, whether it outlived the server that started it, and any failure.",
		input:       sessionIDInputSchema(),
		run: func(ctx context.Context, input SessionStatusInput) (SessionStatusOutput, error) {
			response, err := b.SessionStatus(ctx, input.SessionID)
			if err != nil {
				return SessionStatusOutput{}, err
			}
			return sessionStatusOutput(response), nil
		},
	})
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
			Raw:          result.Raw,
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
		output, err := schema.BindReviewOutput(result.Raw)
		if err != nil {
			continue
		}
		for _, concern := range output.Concerns {
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
		for _, question := range output.Questions {
			triage.Findings = append(triage.Findings, TriageFindingOutput{
				ReviewerName: result.ReviewerName,
				Ref:          question.ID,
				Kind:         "question",
				Title:        question.Topic,
				Detail:       question.WhyItBlocks,
			})
		}
		for _, note := range output.AdvisoryNotes {
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
// questions follow concerns. severity is only meaningful on concerns;
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
		SessionID:  status.SessionID,
		State:      status.State,
		OpenedAt:   status.OpenedAt,
		ClosedAt:   status.ClosedAt,
		RoundCount: status.RoundCount,
		Rounds:     roundStatusOutput(status.Rounds),
	}
}

func roundStatusOutput(rounds []broker.RoundStatus) []RoundStatusOutput {
	out := make([]RoundStatusOutput, 0, len(rounds))
	for _, round := range rounds {
		out = append(out, RoundStatusOutput{
			RoundNumber:    round.RoundNumber,
			OpenedAt:       round.OpenedAt,
			State:          round.State,
			Reviewer:       reviewerInfoOutput(round.Reviewer),
			MaxFindings:    round.MaxFindings,
			StatePath:      round.StatePath,
			LogPath:        round.LogPath,
			CompletedAt:    round.CompletedAt,
			HasNotes:       round.HasNotes,
			DecisionCount:  round.DecisionCount,
			OutlivedServer: round.OutlivedServer,
			Error:          errorOutputPtr(round.Error),
		})
	}
	return out
}

func reviewerInfoOutput(r broker.ReviewerInfo) ReviewerInfoOutput {
	return ReviewerInfoOutput{
		Name:       r.Name,
		Impl:       r.Impl,
		Model:      r.Model,
		BinaryPath: r.BinaryPath,
		ExtraArgs:  append([]string(nil), r.ExtraArgs...),
		Timeout:    r.Timeout.String(),
		Source:     r.Source,
	}
}

func reviewerInfoOutputPtr(r *broker.ReviewerInfo) *ReviewerInfoOutput {
	if r == nil {
		return nil
	}
	out := reviewerInfoOutput(*r)
	return &out
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
		At:      info.At,
	}
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

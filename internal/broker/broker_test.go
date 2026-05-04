package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/michaelquigley/mercurius/internal/reviewer"
	"github.com/michaelquigley/mercurius/internal/roundlog"
	"github.com/michaelquigley/mercurius/internal/schema"
)

func TestSessionRoundsNotesPriorDecisionsAndClose(t *testing.T) {
	ctx := context.Background()
	design := writeArtifactFile(t, "design-v1")
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()})
	b := testBroker(t, r, 3)

	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: design}},
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	round1, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("review round 1: %v", err)
	}
	if round1.RoundNumber != 1 {
		t.Fatalf("round number = %d, want 1", round1.RoundNumber)
	}
	if _, err := os.Stat(round1.LogPath); err != nil {
		t.Fatalf("expected round log: %v", err)
	}
	assertRoundLogStructure(t, round1.LogPath, open.SessionID, 1, []string{"design"}, []string{"dummy"})
	assertSnapshot(t, round1.Manifest[0], "design-v1")

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Prompt == "" || !strings.Contains(req.Prompt, "flag unclear acceptance criteria.") {
		t.Fatal("expected assembled prompt with overrides")
	}
	if len(req.Schema) == 0 {
		t.Fatal("expected schema in reviewer request")
	}
	if req.Artifacts[0].Path != round1.Manifest[0].SnapshotPath {
		t.Fatal("expected reviewer artifact path to point at snapshot")
	}
	if req.SessionMeta.SessionID != open.SessionID || req.SessionMeta.RoundNumber != 1 {
		t.Fatalf("unexpected session metadata: %+v", req.SessionMeta)
	}

	notes, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "commentary",
		Decisions:   []Decision{{Ref: "C-1", Disposition: "accepted", Note: "fix accepted"}},
	})
	if err != nil {
		t.Fatalf("record notes: %v", err)
	}
	if !notes.CommentaryRecorded || notes.DecisionsRecorded != 1 {
		t.Fatalf("unexpected notes response: %+v", notes)
	}
	logContent := readFile(t, round1.LogPath)
	if !strings.Contains(logContent, "notes_recorded: true") || !strings.Contains(logContent, "- **accepted** (ref: C-1): fix accepted.") {
		t.Fatalf("notes not written:\n%s", logContent)
	}

	if err := os.WriteFile(design, []byte("design-v2"), 0o600); err != nil {
		t.Fatalf("edit artifact: %v", err)
	}
	r.push(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	round2, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("review round 2: %v", err)
	}
	assertSnapshot(t, round1.Manifest[0], "design-v1")
	assertSnapshot(t, round2.Manifest[0], "design-v2")
	if round1.Manifest[0].Hash == round2.Manifest[0].Hash {
		t.Fatal("expected distinct hashes after source edit")
	}

	reqs = r.requests()
	if got := reqs[1].SessionMeta.PriorDecisions; len(got) != 1 || got[0].Ref != "C-1" || got[0].RoundNumber != 1 {
		t.Fatalf("unexpected prior decisions: %+v", got)
	}

	status, err := b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RoundsUsed != 2 || !status.Rounds[0].HasNotes || status.Rounds[0].DecisionCount != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}

	closed, err := b.CloseSession(ctx, CloseSessionRequest{SessionID: open.SessionID, Verdict: "ready_to_build"})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Verdict != "ready_to_build" {
		t.Fatalf("unexpected close response: %+v", closed)
	}
	_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeSessionClosed)
}

func TestBudgetExhaustion(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := testBroker(t, r, 1)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID}); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeBudgetExhausted)
}

func TestAtomicFailuresReuseRoundAndBudget(t *testing.T) {
	tests := []struct {
		name string
		resp scriptedResponse
		code string
	}{
		{name: "reviewer error", resp: scriptedResponse{err: errors.New("boom")}, code: CodeReviewerFailed},
		{name: "malformed json", resp: scriptedResponse{raw: json.RawMessage(`{"verdict":`)}, code: CodeSchemaViolation},
		{name: "schema invalid", resp: scriptedResponse{raw: json.RawMessage(`{"not":"valid review output"}`)}, code: CodeSchemaViolation},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			r := newScriptedReviewer(test.resp, scriptedResponse{raw: validReviewOutput("ready_to_build")})
			b := testBroker(t, r, 2)
			open, err := b.OpenSession(ctx, OpenSessionRequest{
				Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
			})
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
			assertBrokerCode(t, err, test.code)
			if _, err := os.Stat(filepath.Join(open.SessionDir, "round-01.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected no round log, got err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(open.SessionDir, "snapshots", "round-01")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected snapshot cleanup, got err=%v", err)
			}
			status, err := b.SessionStatus(ctx, open.SessionID)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if status.RoundsUsed != 0 {
				t.Fatalf("rounds used = %d, want 0", status.RoundsUsed)
			}

			round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
			if err != nil {
				t.Fatalf("subsequent round: %v", err)
			}
			if round.RoundNumber != 1 {
				t.Fatalf("round number after failure = %d, want 1", round.RoundNumber)
			}
		})
	}
}

func TestArtifactOverrideUpdatesOnlyAfterSuccessfulRound(t *testing.T) {
	ctx := context.Background()
	original := writeArtifactFile(t, "original")
	override := writeArtifactFile(t, "override")
	r := newScriptedReviewer(scriptedResponse{err: errors.New("fail override")}, scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := testBroker(t, r, 2)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: original}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	_, err = b.ReviewRound(ctx, ReviewRoundRequest{
		SessionID: open.SessionID,
		Artifacts: []Artifact{{Name: "design", Path: override}},
	})
	assertBrokerCode(t, err, CodeReviewerFailed)

	round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("round after failed override: %v", err)
	}
	if round.Manifest[0].SourcePath != original {
		t.Fatalf("expected original artifact after failed override, got %s", round.Manifest[0].SourcePath)
	}
}

func TestRecordRoundNotesValidationAndReplacement(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: reviewOutputWithRefs()})
	b := testBroker(t, r, 2)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("round: %v", err)
	}

	_, err = b.RecordRoundNotes(ctx, RecordRoundNotesRequest{SessionID: open.SessionID, RoundNumber: 1})
	assertBrokerCode(t, err, CodeEmptyNotes)
	_, err = b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Decisions:   []Decision{{Ref: "missing", Disposition: "accepted", Note: "nope"}},
	})
	assertBrokerCode(t, err, CodeUnknownRef)

	if _, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "first",
		Decisions:   []Decision{{Ref: "Q-1", Disposition: "deferred", Note: "later"}},
	}); err != nil {
		t.Fatalf("record first notes: %v", err)
	}
	if _, err := b.RecordRoundNotes(ctx, RecordRoundNotesRequest{
		SessionID:   open.SessionID,
		RoundNumber: 1,
		Commentary:  "second",
	}); err != nil {
		t.Fatalf("replace notes: %v", err)
	}
	content := readFile(t, round.LogPath)
	if strings.Contains(content, "first") || strings.Contains(content, "deferred") {
		t.Fatalf("old notes remained:\n%s", content)
	}
	if !strings.Contains(content, "second") || !strings.Contains(content, "_no decisions recorded yet_") {
		t.Fatalf("replacement not written:\n%s", content)
	}
	if strings.Count(content, roundlog.NotesBeginMarker) != 1 || strings.Count(content, roundlog.NotesEndMarker) != 1 {
		t.Fatal("expected one notes region")
	}
}

func TestOpenSessionArtifactAndLogDestinationValidation(t *testing.T) {
	ctx := context.Background()
	validPath := writeArtifactFile(t, "content")

	tests := []struct {
		name      string
		artifacts []Artifact
	}{
		{name: "empty", artifacts: nil},
		{name: "duplicate", artifacts: []Artifact{{Name: "design", Path: validPath}, {Name: "design", Path: validPath}}},
		{name: "slash", artifacts: []Artifact{{Name: "bad/name", Path: validPath}}},
		{name: "dot", artifacts: []Artifact{{Name: ".", Path: validPath}}},
		{name: "non absolute", artifacts: []Artifact{{Name: "design", Path: "design.md"}}},
		{name: "missing", artifacts: []Artifact{{Name: "design", Path: filepath.Join(t.TempDir(), "missing.md")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := testBroker(t, newScriptedReviewer(), 1)
			_, err := b.OpenSession(ctx, OpenSessionRequest{Artifacts: test.artifacts})
			assertBrokerCode(t, err, CodeInvalidArtifacts)
		})
	}

	b := New(Options{
		LogDestination: filepath.Join(t.TempDir(), "missing-parent", "reviews"),
		DefaultBudget:  1,
		Reviewers: []ReviewerSpec{{
			Name:    "dummy",
			Factory: func(string) reviewer.Reviewer { return newScriptedReviewer() },
		}},
	})
	_, err := b.OpenSession(ctx, OpenSessionRequest{Artifacts: []Artifact{{Name: "design", Path: validPath}}})
	assertBrokerCode(t, err, CodeInvalidLogDestination)
}

func TestInlineArtifactSnapshotAndPrompt(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := testBroker(t, r, 1)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "context", Path: "ignored-relative-path", Content: []byte("inline content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	round, err := b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	if err != nil {
		t.Fatalf("round: %v", err)
	}
	if !round.Manifest[0].Inline || round.Manifest[0].SourcePath != "" {
		t.Fatalf("unexpected inline manifest: %+v", round.Manifest[0])
	}
	assertSnapshot(t, round.Manifest[0], "inline content")
	if !strings.Contains(readFile(t, round.LogPath), "| context | null |") {
		t.Fatal("expected null source path in log")
	}
	if !strings.Contains(r.requests()[0].Prompt, "Source path: inline") {
		t.Fatal("expected inline source path in prompt")
	}
}

func TestRoundLogWriteFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	r := newScriptedReviewer(scriptedResponse{raw: validReviewOutput("ready_to_build")})
	b := testBroker(t, r, 2)
	open, err := b.OpenSession(ctx, OpenSessionRequest{
		Artifacts: []Artifact{{Name: "design", Path: writeArtifactFile(t, "content")}},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := os.Mkdir(filepath.Join(open.SessionDir, "round-01.md"), 0o700); err != nil {
		t.Fatalf("create log path directory: %v", err)
	}

	_, err = b.ReviewRound(ctx, ReviewRoundRequest{SessionID: open.SessionID})
	assertBrokerCode(t, err, CodeInternalError)
	if _, err := os.Stat(filepath.Join(open.SessionDir, "snapshots", "round-01")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected snapshot cleanup, got err=%v", err)
	}
	status, err := b.SessionStatus(ctx, open.SessionID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RoundsUsed != 0 {
		t.Fatalf("rounds used = %d, want 0", status.RoundsUsed)
	}
}

type scriptedResponse struct {
	raw json.RawMessage
	err error
}

type scriptedReviewer struct {
	mu        sync.Mutex
	responses []scriptedResponse
	captured  []reviewer.ReviewRequest
}

func newScriptedReviewer(responses ...scriptedResponse) *scriptedReviewer {
	return &scriptedReviewer{responses: append([]scriptedResponse(nil), responses...)}
}

func (r *scriptedReviewer) Review(ctx context.Context, req reviewer.ReviewRequest) (reviewer.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return reviewer.ReviewResponse{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.captured = append(r.captured, cloneReviewerRequest(req))
	if len(r.responses) == 0 {
		return reviewer.ReviewResponse{Raw: validReviewOutput("ready_to_build"), UsageNotes: "scripted"}, nil
	}
	resp := r.responses[0]
	r.responses = r.responses[1:]
	if resp.err != nil {
		return reviewer.ReviewResponse{}, resp.err
	}
	return reviewer.ReviewResponse{Raw: append(json.RawMessage(nil), resp.raw...), UsageNotes: "scripted"}, nil
}

func (r *scriptedReviewer) push(response scriptedResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses = append(r.responses, response)
}

func (r *scriptedReviewer) requests() []reviewer.ReviewRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	requests := make([]reviewer.ReviewRequest, 0, len(r.captured))
	for _, req := range r.captured {
		requests = append(requests, cloneReviewerRequest(req))
	}
	return requests
}

func testBroker(t *testing.T, r *scriptedReviewer, budget int) *Broker {
	t.Helper()

	return New(Options{
		LogDestination:  filepath.Join(t.TempDir(), "reviews"),
		DefaultBudget:   budget,
		PromptOverrides: "flag unclear acceptance criteria.",
		Reviewers: []ReviewerSpec{{
			Name:    "dummy",
			Factory: func(string) reviewer.Reviewer { return r },
		}},
	})
}

func writeArtifactFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "artifact.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

func assertSnapshot(t *testing.T, manifest ArtifactManifestEntry, want string) {
	t.Helper()

	got := readFile(t, manifest.SnapshotPath)
	if got != want {
		t.Fatalf("snapshot content = %q, want %q", got, want)
	}
	if manifest.Size != int64(len(want)) {
		t.Fatalf("snapshot size = %d, want %d", manifest.Size, len(want))
	}
	if !strings.HasPrefix(manifest.Hash, "sha256:") {
		t.Fatalf("expected sha256 hash, got %s", manifest.Hash)
	}
}

func assertRoundLogStructure(t *testing.T, path string, sessionID string, roundNumber int, artifactNames []string, reviewerNames []string) {
	t.Helper()

	content := readFile(t, path)
	parts := strings.SplitN(content, "---\n", 3)
	if len(parts) != 3 {
		t.Fatalf("round log missing frontmatter:\n%s", content)
	}
	frontmatter := parseFrontmatter(parts[1])
	if frontmatter["session_id"] != sessionID {
		t.Fatalf("frontmatter session_id = %q, want %q", frontmatter["session_id"], sessionID)
	}
	if frontmatter["round_number"] != strconv.Itoa(roundNumber) {
		t.Fatalf("frontmatter round_number = %q, want %d", frontmatter["round_number"], roundNumber)
	}
	if frontmatter["notes_recorded"] != "false" {
		t.Fatalf("frontmatter notes_recorded = %q, want false", frontmatter["notes_recorded"])
	}
	if !strings.Contains(parts[2], "## Artifact manifest\n\n| name | source_path | snapshot_path | size | hash |") {
		t.Fatal("round log missing artifact manifest table")
	}
	for _, name := range artifactNames {
		if !strings.Contains(parts[2], "| "+name+" |") {
			t.Fatalf("round log missing artifact row for %q", name)
		}
	}
	for _, name := range reviewerNames {
		if !strings.Contains(parts[2], "### "+name+"\n\n**Usage notes:**") {
			t.Fatalf("round log missing reviewer block for %q", name)
		}
	}
	if strings.Count(parts[2], roundlog.NotesBeginMarker) != 1 || strings.Count(parts[2], roundlog.NotesEndMarker) != 1 {
		t.Fatal("round log must contain exactly one notes marker pair")
	}
}

func parseFrontmatter(frontmatter string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(frontmatter, "\n") {
		if strings.HasPrefix(line, " ") || !strings.Contains(line, ":") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	return values
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func assertBrokerCode(t *testing.T, err error, code string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	if got := ErrorCode(err); got != code {
		t.Fatalf("error code = %s, want %s; err=%v", got, code, err)
	}
}

func validReviewOutput(verdict string) json.RawMessage {
	raw, err := json.Marshal(schema.ReviewOutput{
		Verdict:       verdict,
		Summary:       "reviewed",
		Concerns:      []schema.Concern{},
		Questions:     []schema.Question{},
		ProposedDiffs: []schema.ProposedDiff{},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func reviewOutputWithRefs() json.RawMessage {
	suggestion := "clarify the work order"
	raw, err := json.Marshal(schema.ReviewOutput{
		Verdict: "needs_changes",
		Summary: "reviewed",
		Concerns: []schema.Concern{
			{ID: "C-1", Severity: "major", Location: "work-order:M3", Claim: "missing detail", Rationale: "implementation would diverge", Suggestion: &suggestion},
		},
		Questions: []schema.Question{
			{ID: "Q-1", Topic: "budget", WhyItBlocks: "cannot judge loop length"},
		},
		ProposedDiffs: []schema.ProposedDiff{},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func cloneReviewerRequest(req reviewer.ReviewRequest) reviewer.ReviewRequest {
	req.Artifacts = append([]reviewer.Artifact(nil), req.Artifacts...)
	for i := range req.Artifacts {
		req.Artifacts[i].Content = append([]byte(nil), req.Artifacts[i].Content...)
	}
	req.Schema = append(json.RawMessage(nil), req.Schema...)
	req.SessionMeta.PriorDecisions = append([]reviewer.PriorDecision(nil), req.SessionMeta.PriorDecisions...)
	return req
}

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/prompt"
	"github.com/michaelquigley/mercurius/internal/reviewer"
	monitorpkg "github.com/michaelquigley/mercurius/internal/monitor"
	"github.com/spf13/cobra"
)

func TestPrintStatusSuppressesEmptyConvergence(t *testing.T) {
	cmd, out := testCommand()
	now := time.Date(2026, 5, 5, 19, 38, 10, 0, time.UTC)

	printStatus(cmd, monitorpkg.SessionStatus{
		SessionID:            "s_test",
		State:                "active",
		Budget:               4,
		BudgetRemaining:      4,
		ReviewContextSource:  "config",
		ReviewContextPresent: true,
		Convergence:          monitorpkg.Convergence{Signal: "none", Message: "No convergence signal yet."},
		ActiveRound: &monitorpkg.RoundJob{
			SessionID:   "s_test",
			RoundNumber: 1,
			State:       "running",
			Reviewer:    "codex",
			StartedAt:   now,
			StatusPath:  "/tmp/status.json",
			EventsPath:  "/tmp/events.ndjson",
		},
	})

	got := out.String()
	for _, want := range []string{
		"session 's_test' active",
		"review context: config",
		"active round: 1 running reviewer='codex'",
		"monitor files: status='/tmp/status.json' events='/tmp/events.ndjson'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected monitor status to contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "convergence") {
		t.Fatalf("empty convergence should be suppressed:\n%s", got)
	}
}

func TestPrintRoundTerminalActionCompleted(t *testing.T) {
	cmd, out := testCommand()

	printRoundTerminalAction(cmd, monitorpkg.Event{
		Event:       "round_completed",
		SessionID:   "s_test",
		RoundNumber: 2,
		LogPath:     "/tmp/round-02.md",
	})

	got := out.String()
	for _, want := range []string{
		"round 2 completed",
		"log: '/tmp/round-02.md'",
		"next: ask the design agent to call collect_round for session 's_test' round 2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected terminal action to contain %q:\n%s", want, got)
		}
	}
}

func TestPreviewParsesArtifacts(t *testing.T) {
	parsed, err := parsePreviewArtifacts([]string{"design=/abs/path", "work-order=./relative=with=equals"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed) != 2 || parsed[0].name != "design" || parsed[0].path != "/abs/path" {
		t.Fatalf("first parsed: %+v", parsed[0])
	}
	if parsed[1].name != "work-order" || parsed[1].path != "./relative=with=equals" {
		t.Fatalf("path with embedded '=' lost characters: %+v", parsed[1])
	}
}

func TestPreviewRejectsInvalidArtifactSpec(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "no equals", args: []string{"design"}},
		{name: "empty name", args: []string{"=foo"}},
		{name: "empty path", args: []string{"design="}},
		{name: "leading underscore", args: []string{"_design=/x"}},
		{name: "duplicate", args: []string{"design=/a", "design=/b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePreviewArtifacts(tc.args); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPreviewProducesPromptByteEqualToBuild(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writePreviewConfig(t, tmp, `
log_destination: ./reviews
review_focus: |
  flag silent failures.
review_context: |
  deployment: personal one-shot
reviewers:
  - name: dummy
    impl: dummy
`)
	artifactPath := filepath.Join(tmp, "design.md")
	if err := os.WriteFile(artifactPath, []byte("# design\n"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	cmd, out := testCommand()
	cmd.SetArgs([]string{"preview", "--config", cfgPath, "--artifact", "design=" + artifactPath})
	cmd.AddCommand(newPreviewCommand(stringPtr(cfgPath)))
	if err := runPreview(cfgPath, "design="+artifactPath, "", "", 0, ""); err != nil {
		t.Fatalf("run preview: %v", err)
	}
	got := out.String()
	if got != "" {
		t.Fatalf("expected empty cobra buffer when invoked via runPreview; instead got:\n%s", got)
	}

	// independently build what Build() would emit for the same inputs and
	// compare byte-for-byte.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	specs, err := parsePreviewArtifacts([]string{"design=" + artifactPath})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req, err := buildPreviewContext(cfg, specs, "", "", 0)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	wantText, _ := prompt.Build(req)

	previewText := capturePreviewOutput(t, cfgPath, []string{"design=" + artifactPath}, "", "", 0)
	if previewText != wantText {
		t.Fatalf("preview output drifted from prompt.Build output")
	}

	// snapshot path sentinel must be the only difference between preview and
	// broker round 1's prompt.
	brokerText := captureBrokerRound1Prompt(t, cfg, []broker.Artifact{{Name: "design", Path: artifactPath}})
	hash := sha256.Sum256([]byte("# design\n"))
	hashStr := "sha256:" + hex.EncodeToString(hash[:])
	previewNormalized := strings.Replace(previewText, "Snapshot path: "+previewSnapshotSentinel, "Snapshot path: __NORMALIZED__", 1)
	brokerNormalized := normalizeBrokerSnapshot(brokerText, "design", hashStr)
	if previewNormalized != brokerNormalized {
		t.Fatalf("preview and broker round 1 prompts differ beyond the snapshot path sentinel")
	}
}

func TestPreviewDoesNotCreateLogDestination(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writePreviewConfig(t, tmp, `
log_destination: ./reviews
reviewers:
  - name: dummy
    impl: dummy
`)
	artifactPath := filepath.Join(tmp, "design.md")
	if err := os.WriteFile(artifactPath, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := runPreview(cfgPath, "design="+artifactPath, "", "", 0, ""); err != nil {
		t.Fatalf("run preview: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "reviews")); !os.IsNotExist(err) {
		t.Fatalf("preview created log_destination as a side effect: stat err=%v", err)
	}
}

func TestMonitorDoesNotCreateLogDestination(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writePreviewConfig(t, tmp, `
log_destination: ./reviews
reviewers:
  - name: dummy
    impl: dummy
`)
	root := newRootCommand()
	cmd, out := testCommand()
	cmd.SetArgs([]string{"monitor", "--config", cfgPath, "--all"})
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"monitor", "--config", cfgPath, "--all"})
	// monitor against a missing log_destination must error cleanly without
	// creating the directory.
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected monitor against missing log_destination to error")
	}
	if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("expected not-exist error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "reviews")); !os.IsNotExist(err) {
		t.Fatalf("monitor created log_destination as a side effect: stat err=%v", err)
	}
}

func writePreviewConfig(t *testing.T, dir string, body string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runPreview(cfgPath string, artifact string, reviewContext string, reviewFocus string, maxFindings int, output string) error {
	configPath := cfgPath
	cmd := newPreviewCommand(&configPath)
	args := []string{"--artifact", artifact}
	if reviewContext != "" {
		args = append(args, "--review-context", reviewContext)
	}
	if reviewFocus != "" {
		args = append(args, "--review-focus", reviewFocus)
	}
	if maxFindings > 0 {
		args = append(args, "--max-findings", fmtInt(maxFindings))
	}
	if output != "" {
		args = append(args, "--output", output)
	}
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	return cmd.Execute()
}

func capturePreviewOutput(t *testing.T, cfgPath string, artifacts []string, reviewContext string, reviewFocus string, maxFindings int) string {
	t.Helper()
	configPath := cfgPath
	cmd := newPreviewCommand(&configPath)
	args := []string{}
	for _, a := range artifacts {
		args = append(args, "--artifact", a)
	}
	if reviewContext != "" {
		args = append(args, "--review-context", reviewContext)
	}
	if reviewFocus != "" {
		args = append(args, "--review-focus", reviewFocus)
	}
	if maxFindings > 0 {
		args = append(args, "--max-findings", fmtInt(maxFindings))
	}
	cmd.SetArgs(args)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("preview: %v", err)
	}
	return buf.String()
}

func captureBrokerRound1Prompt(t *testing.T, cfg *config.Config, artifacts []broker.Artifact) string {
	t.Helper()
	// reproduce the prompt construction broker round 1 performs for an empty
	// session, but skip the reviewer dispatch and snapshotting machinery.
	promptArtifacts := make([]prompt.Artifact, 0, len(artifacts))
	for _, a := range artifacts {
		content, err := os.ReadFile(a.Path)
		if err != nil {
			t.Fatalf("read artifact: %v", err)
		}
		hash := sha256.Sum256(content)
		promptArtifacts = append(promptArtifacts, prompt.Artifact{
			Name:         a.Name,
			SourcePath:   a.Path,
			SnapshotPath: "/synthetic-snapshot/" + a.Name,
			Hash:         "sha256:" + hex.EncodeToString(hash[:]),
			Content:      content,
		})
	}
	text, _ := prompt.Build(prompt.Request{
		Artifacts:      promptArtifacts,
		PriorDecisions: []reviewer.PriorDecision(nil),
		ReviewContext:  cfg.ReviewContext,
		ReviewFocus:    cfg.ReviewFocus,
		DecisionsLog:   broker.EmptySessionDecisionsLogText(),
		MaxFindings:    cfg.MaxFindings,
	})
	return text
}

func normalizeBrokerSnapshot(text string, artifactName string, hash string) string {
	// replace the broker-side synthetic snapshot path with the same sentinel
	// the preview path uses, so the only meaningful difference between the
	// two prompts is removed for the byte-equality check.
	return strings.Replace(text, "Snapshot path: /synthetic-snapshot/"+artifactName, "Snapshot path: __NORMALIZED__", 1)
}

func stringPtr(s string) *string { return &s }

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	if n < 0 {
		digits = append(digits, '-')
		n = -n
	}
	var rev []byte
	for n > 0 {
		rev = append(rev, byte('0'+n%10))
		n /= 10
	}
	for i := len(rev) - 1; i >= 0; i-- {
		digits = append(digits, rev[i])
	}
	return string(digits)
}

func testCommand() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	return cmd, &out
}

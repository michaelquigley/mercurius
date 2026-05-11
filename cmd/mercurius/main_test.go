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
	monitorpkg "github.com/michaelquigley/mercurius/internal/monitor"
	"github.com/michaelquigley/mercurius/internal/prompt"
	"github.com/spf13/cobra"
)

func TestPrintStatusOmitsRemovedFields(t *testing.T) {
	cmd, out := testCommand()
	now := time.Date(2026, 5, 5, 19, 38, 10, 0, time.UTC)

	printStatus(cmd, monitorpkg.SessionStatus{
		SessionID:            "s_test",
		State:                "active",
		RoundCount:           1,
		ReviewContextPresent: true,
		ReviewFocusPresent:   true,
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
		"rounds: 1",
		"review context: present",
		"review focus: present",
		"active round: 1 running reviewer='codex'",
		"monitor files: status='/tmp/status.json' events='/tmp/events.ndjson'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected monitor status to contain %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"budget", "convergence", "verdict"} {
		if strings.Contains(got, banned) {
			t.Fatalf("status should not contain %q:\n%s", banned, got)
		}
	}
}

func TestPrintRoundTerminalActionCompleted(t *testing.T) {
	cmd, out := testCommand()

	printRoundTerminalAction(cmd, monitorpkg.Event{
		Event:       "round_completed",
		SessionID:   "s_test",
		RoundNumber: 2,
		LogPath:     "/tmp/round-02/_round.md",
	})

	got := out.String()
	for _, want := range []string{
		"round 2 completed",
		"log: '/tmp/round-02/_round.md'",
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

	if err := runPreview(cfgPath, "design="+artifactPath, ""); err != nil {
		t.Fatalf("run preview: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	specs, err := parsePreviewArtifacts([]string{"design=" + artifactPath})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req, err := buildPreviewContext(cfg, specs)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	wantText, _ := prompt.Build(req)

	previewText := capturePreviewOutput(t, cfgPath, []string{"design=" + artifactPath})
	if previewText != wantText {
		t.Fatalf("preview output drifted from prompt.Build output")
	}

	// snapshot path sentinel must be the only difference between preview and
	// broker round 1's prompt.
	brokerText := captureBrokerRound1Prompt(t, cfg, []broker.Artifact{{Name: "design", Path: artifactPath}})
	previewNormalized := strings.Replace(previewText, "Snapshot path: "+previewSnapshotSentinel, "Snapshot path: __NORMALIZED__", 1)
	brokerNormalized := strings.Replace(brokerText, "Snapshot path: /synthetic-snapshot/design", "Snapshot path: __NORMALIZED__", 1)
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
	if err := runPreview(cfgPath, "design="+artifactPath, ""); err != nil {
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
	_, out := testCommand()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"monitor", "--config", cfgPath, "--all"})
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

func runPreview(cfgPath string, artifact string, output string) error {
	configPath := cfgPath
	cmd := newPreviewCommand(&configPath)
	args := []string{"--artifact", artifact}
	if output != "" {
		args = append(args, "--output", output)
	}
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	return cmd.Execute()
}

func capturePreviewOutput(t *testing.T, cfgPath string, artifacts []string) string {
	t.Helper()
	configPath := cfgPath
	cmd := newPreviewCommand(&configPath)
	args := []string{}
	for _, a := range artifacts {
		args = append(args, "--artifact", a)
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
		Artifacts:     promptArtifacts,
		ReviewContext: cfg.ReviewContext,
		ReviewFocus:   cfg.ReviewFocus,
		MaxFindings:   cfg.MaxFindings,
	})
	return text
}

func testCommand() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	return cmd, &out
}

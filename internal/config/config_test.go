package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveResolvesDefaultsAndPaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
review_focus: |
  flag unclear logging.
review_context: |
  deployment: personal one-shot
reviewer:
  name: codex
  impl: codex
  binary_path: ./bin/codex
  model: gpt-test
  extra_args:
    - --ignore-user-config
`)

	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	cfg := resolved.Project
	if cfg.MaxFindings != DefaultMaxFindings {
		t.Fatalf("max findings = %d, want %d", cfg.MaxFindings, DefaultMaxFindings)
	}
	if cfg.LogDestination != filepath.Join(dir, "reviews") {
		t.Fatalf("log destination = %s", cfg.LogDestination)
	}
	if cfg.ConfigPath != cfgPath || cfg.Name != filepath.Base(dir) {
		t.Fatalf("config path = %q, name = %q", cfg.ConfigPath, cfg.Name)
	}
	if _, err := os.Stat(cfg.LogDestination); !os.IsNotExist(err) {
		t.Fatalf("Resolve() created log destination as a side effect: stat err=%v", err)
	}
	if err := EnsureLogDestination(cfg.LogDestination); err != nil {
		t.Fatalf("ensure log destination: %v", err)
	}
	if _, err := os.Stat(cfg.LogDestination); err != nil {
		t.Fatalf("expected log destination to be created by EnsureLogDestination: %v", err)
	}
	reviewer := resolved.Reviewer
	if reviewer == nil {
		t.Fatal("expected reviewer to be populated")
	}
	if reviewer.BinaryPath != filepath.Join(dir, "bin", "codex") {
		t.Fatalf("binary path = %s", reviewer.BinaryPath)
	}
	if reviewer.Timeout != DefaultReviewerTimeout {
		t.Fatalf("timeout = %s, want the default %s", reviewer.Timeout, DefaultReviewerTimeout)
	}
	if !strings.Contains(cfg.ReviewContext, "deployment: personal one-shot") {
		t.Fatalf("review context = %q", cfg.ReviewContext)
	}
	if !strings.Contains(cfg.ReviewFocus, "flag unclear logging.") {
		t.Fatalf("review focus = %q", cfg.ReviewFocus)
	}
	if reviewer.ExtraArgs[0] != "--ignore-user-config" {
		t.Fatalf("extra args not loaded: %+v", reviewer.ExtraArgs)
	}
}

func TestResolveRejectsRenamedPromptOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
prompt_overrides: |
  stale value
reviewer:
  name: dummy
  impl: dummy
`)

	_, err := Resolve(cfgPath, "")
	if err == nil {
		t.Fatal("expected rename-guidance error")
	}
	if !strings.Contains(err.Error(), "prompt_overrides") || !strings.Contains(err.Error(), "review_focus") {
		t.Fatalf("expected error to name both old and new field, got %v", err)
	}
}

func TestResolveRejectsRenamedReviewers(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
reviewers:
  - name: dummy
    impl: dummy
`)

	_, err := Resolve(cfgPath, "")
	if err == nil {
		t.Fatal("expected rename-guidance error")
	}
	if !strings.Contains(err.Error(), "reviewers") || !strings.Contains(err.Error(), "reviewer") {
		t.Fatalf("expected error to name both old and new field, got %v", err)
	}
}

func TestResolveConfiguresMaxFindings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
max_findings: 6
reviewer:
  name: dummy
  impl: dummy
`)

	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if resolved.Project.MaxFindings != 6 {
		t.Fatalf("max findings = %d, want 6", resolved.Project.MaxFindings)
	}
}

func TestResolveAcceptsKnownImpls(t *testing.T) {
	cases := []struct {
		impl  string
		model string
	}{
		{impl: "codex", model: "gpt-5.5"},
		{impl: "claude", model: "sonnet"},
		{impl: "pi", model: "openai-codex/gpt-5.5"},
		{impl: "dummy"},
	}
	for _, test := range cases {
		t.Run(test.impl, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mercurius.yaml")
			body := "\nlog_destination: ./reviews\nreviewer:\n  name: " + test.impl + "\n  impl: " + test.impl + "\n"
			if test.model != "" {
				body += "  model: " + test.model + "\n"
			}
			writeConfig(t, path, body)
			resolved, err := Resolve(path, "")
			if err != nil {
				t.Fatalf("resolve config: %v", err)
			}
			if resolved.Reviewer.Impl != test.impl {
				t.Fatalf("impl = %q, want %q", resolved.Reviewer.Impl, test.impl)
			}
			if resolved.Reviewer.Model != test.model {
				t.Fatalf("model = %q, want %q", resolved.Reviewer.Model, test.model)
			}
		})
	}
}

func TestResolveValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "removed default_budget rejected",
			body: `
log_destination: ./reviews
default_budget: 4
reviewer:
  name: dummy
  impl: dummy
`,
			want: "default_budget",
		},
		{
			name: "invalid max findings",
			body: `
log_destination: ./reviews
max_findings: 0
reviewer:
  name: dummy
  impl: dummy
`,
			want: "max_findings",
		},
		{
			name: "unknown impl",
			body: `
log_destination: ./reviews
reviewer:
  name: nope
  impl: nope
`,
			want: "unknown impl",
		},
		{
			name: "negative timeout",
			body: `
log_destination: ./reviews
reviewer:
  name: dummy
  impl: dummy
  timeout: -1m
`,
			want: "timeout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mercurius.yaml")
			writeConfig(t, path, test.body)
			_, err := Resolve(path, "")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q in error, got %v", test.want, err)
			}
		})
	}
}

func TestEnsureLogDestinationCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
reviewer:
  name: dummy
  impl: dummy
`)
	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := EnsureLogDestination(resolved.Project.LogDestination); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := os.Stat(resolved.Project.LogDestination); err != nil {
		t.Fatalf("expected directory to exist: %v", err)
	}
}

func TestEnsureLogDestinationRejectsMissingParent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./missing/reviews
reviewer:
  name: dummy
  impl: dummy
`)
	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	err = EnsureLogDestination(resolved.Project.LogDestination)
	if err == nil {
		t.Fatal("expected missing-parent error")
	}
	if !strings.Contains(err.Error(), "parent") {
		t.Fatalf("expected parent in error, got %v", err)
	}
}

func TestResolveHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := resolvePath(t.TempDir(), "~/reviews")
	if got != filepath.Join(home, "reviews") {
		t.Fatalf("home path = %s", got)
	}
}

func TestResolveParsesSettledDecisions(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
settled_decisions:
  - id: recall-deferred
    do_not_flag: >
      the absence of a 'recall' concept, or suggestions to add it now
  - id: observability-out-of-scope
    do_not_flag: missing production-grade observability
reviewer:
  name: dummy
  impl: dummy
`)

	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	cfg := resolved.Project
	if len(cfg.SettledDecisions) != 2 {
		t.Fatalf("settled decisions = %d, want 2", len(cfg.SettledDecisions))
	}
	if cfg.SettledDecisions[0].ID != "recall-deferred" {
		t.Fatalf("first id = %q", cfg.SettledDecisions[0].ID)
	}
	if !strings.Contains(cfg.SettledDecisions[0].DoNotFlag, "absence of a 'recall' concept") {
		t.Fatalf("first do_not_flag = %q", cfg.SettledDecisions[0].DoNotFlag)
	}
	if cfg.SettledDecisions[1].DoNotFlag != "missing production-grade observability" {
		t.Fatalf("second do_not_flag = %q", cfg.SettledDecisions[1].DoNotFlag)
	}
}

func TestResolveToleratesEmptyAndDuplicateGuards(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
settled_decisions:
  - id: dupe
    do_not_flag: first
  - id: dupe
    do_not_flag: ""
  - id: blank
reviewer:
  name: dummy
  impl: dummy
`)

	// an empty do_not_flag and duplicate ids must not fail the load: a guard
	// should cost almost nothing to write, and a too-strict validator would
	// fail rounds on trivial guard typos.
	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if len(resolved.Project.SettledDecisions) != 3 {
		t.Fatalf("settled decisions = %d, want 3 (all tolerated)", len(resolved.Project.SettledDecisions))
	}
}

func TestResolveReturnsExactBytes(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	body := `log_destination: ./reviews
settled_decisions:
  - id: recall-deferred
    do_not_flag: the absence of a 'recall' concept
reviewer:
  name: dummy
  impl: dummy
`
	writeConfig(t, cfgPath, body)

	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(resolved.Raw) != body {
		t.Fatalf("raw bytes do not match file content:\n--- got ---\n%s\n--- want ---\n%s", resolved.Raw, body)
	}
}

// a project with no reviewer resolves: open_session reports none, and only
// start_review_round turns the absence into an error.
func TestResolveWithoutReviewer(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "mercurius.yaml")
	writeConfig(t, cfgPath, "log_destination: ./reviews\n")
	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Reviewer != nil {
		t.Fatalf("reviewer = %+v, want none", resolved.Reviewer)
	}
}

func TestResolveParsesReviewerTimeout(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "mercurius.yaml")
	writeConfig(t, cfgPath, "reviewer:\n  name: dummy\n  impl: dummy\n  timeout: 7m30s\n")
	resolved, err := Resolve(cfgPath, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Reviewer.Timeout != 7*time.Minute+30*time.Second {
		t.Fatalf("timeout = %s, want 7m30s", resolved.Reviewer.Timeout)
	}
}

func TestResolveMissingFileNamesPath(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "mercurius.yaml")
	_, err := Resolve(cfgPath, "")
	if err == nil {
		t.Fatal("expected an error for a missing config")
	}
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), cfgPath) {
		t.Fatalf("expected a not-exist error naming '%s', got %v", cfgPath, err)
	}
}

// the user-level path is accepted and not read yet: a broken file there must
// not affect resolution.
func TestResolveDoesNotReadUserConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	userPath := filepath.Join(dir, "user.yaml")
	writeConfig(t, cfgPath, "reviewer:\n  name: dummy\n  impl: dummy\n")
	writeConfig(t, userPath, "this: is: not: valid: yaml\n\t::broken")
	if _, err := Resolve(cfgPath, userPath); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

func writeConfig(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

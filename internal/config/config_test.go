package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadResolvesDefaultsAndPaths(t *testing.T) {
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

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.MaxFindings != DefaultMaxFindings {
		t.Fatalf("max findings = %d, want %d", cfg.MaxFindings, DefaultMaxFindings)
	}
	if cfg.LogDestination != filepath.Join(dir, "reviews") {
		t.Fatalf("log destination = %s", cfg.LogDestination)
	}
	if _, err := os.Stat(cfg.LogDestination); !os.IsNotExist(err) {
		t.Fatalf("Load() created log destination as a side effect: stat err=%v", err)
	}
	if err := cfg.EnsureLogDestination(); err != nil {
		t.Fatalf("ensure log destination: %v", err)
	}
	if _, err := os.Stat(cfg.LogDestination); err != nil {
		t.Fatalf("expected log destination to be created by EnsureLogDestination: %v", err)
	}
	if cfg.Reviewer == nil {
		t.Fatal("expected reviewer to be populated")
	}
	if cfg.Reviewer.BinaryPath != filepath.Join(dir, "bin", "codex") {
		t.Fatalf("binary path = %s", cfg.Reviewer.BinaryPath)
	}
	if !strings.Contains(cfg.ReviewContext, "deployment: personal one-shot") {
		t.Fatalf("review context = %q", cfg.ReviewContext)
	}
	if !strings.Contains(cfg.ReviewFocus, "flag unclear logging.") {
		t.Fatalf("review focus = %q", cfg.ReviewFocus)
	}
	if cfg.Reviewer.ExtraArgs[0] != "--ignore-user-config" {
		t.Fatalf("extra args not loaded: %+v", cfg.Reviewer.ExtraArgs)
	}
}

func TestLoadRejectsRenamedPromptOverrides(t *testing.T) {
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

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected rename-guidance error")
	}
	if !strings.Contains(err.Error(), "prompt_overrides") || !strings.Contains(err.Error(), "review_focus") {
		t.Fatalf("expected error to name both old and new field, got %v", err)
	}
}

func TestLoadRejectsRenamedReviewers(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
reviewers:
  - name: dummy
    impl: dummy
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected rename-guidance error")
	}
	if !strings.Contains(err.Error(), "reviewers") || !strings.Contains(err.Error(), "reviewer") {
		t.Fatalf("expected error to name both old and new field, got %v", err)
	}
}

func TestLoadConfiguresMaxFindings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
max_findings: 6
reviewer:
  name: dummy
  impl: dummy
`)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.MaxFindings != 6 {
		t.Fatalf("max findings = %d, want 6", cfg.MaxFindings)
	}
}

func TestLoadAcceptsKnownImpls(t *testing.T) {
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
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Reviewer.Impl != test.impl {
				t.Fatalf("impl = %q, want %q", cfg.Reviewer.Impl, test.impl)
			}
			if cfg.Reviewer.Model != test.model {
				t.Fatalf("model = %q, want %q", cfg.Reviewer.Model, test.model)
			}
		})
	}
}

func TestLoadValidatesConfig(t *testing.T) {
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
			name: "missing reviewer",
			body: `
log_destination: ./reviews
`,
			want: "reviewer is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mercurius.yaml")
			writeConfig(t, path, test.body)
			_, err := Load(path)
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
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.EnsureLogDestination(); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := os.Stat(cfg.LogDestination); err != nil {
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
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = cfg.EnsureLogDestination()
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

func TestLoadParsesSettledDecisions(t *testing.T) {
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

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
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

func TestLoadToleratesEmptyAndDuplicateGuards(t *testing.T) {
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
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.SettledDecisions) != 3 {
		t.Fatalf("settled decisions = %d, want 3 (all tolerated)", len(cfg.SettledDecisions))
	}
}

func TestLoadWithRawReturnsExactBytes(t *testing.T) {
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

	cfg, raw, err := LoadWithRaw(cfgPath)
	if err != nil {
		t.Fatalf("load with raw: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config")
	}
	if string(raw) != body {
		t.Fatalf("raw bytes do not match file content:\n--- got ---\n%s\n--- want ---\n%s", raw, body)
	}
}

func writeConfig(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

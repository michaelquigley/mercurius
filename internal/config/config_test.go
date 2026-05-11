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
reviewers:
  - name: codex
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
	// Load() is pure-validation: the directory must NOT have been created yet.
	if _, err := os.Stat(cfg.LogDestination); !os.IsNotExist(err) {
		t.Fatalf("Load() created log destination as a side effect: stat err=%v", err)
	}
	if err := cfg.EnsureLogDestination(); err != nil {
		t.Fatalf("ensure log destination: %v", err)
	}
	if _, err := os.Stat(cfg.LogDestination); err != nil {
		t.Fatalf("expected log destination to be created by EnsureLogDestination: %v", err)
	}
	if cfg.Reviewers[0].BinaryPath != filepath.Join(dir, "bin", "codex") {
		t.Fatalf("binary path = %s", cfg.Reviewers[0].BinaryPath)
	}
	if !strings.Contains(cfg.ReviewContext, "deployment: personal one-shot") {
		t.Fatalf("review context = %q", cfg.ReviewContext)
	}
	if !strings.Contains(cfg.ReviewFocus, "flag unclear logging.") {
		t.Fatalf("review focus = %q", cfg.ReviewFocus)
	}
	if cfg.Reviewers[0].ExtraArgs[0] != "--ignore-user-config" {
		t.Fatalf("extra args not loaded: %+v", cfg.Reviewers[0].ExtraArgs)
	}
}

func TestLoadRejectsRenamedPromptOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
prompt_overrides: |
  stale value
reviewers:
  - name: dummy
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

func TestLoadConfiguresMaxFindings(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
max_findings: 6
reviewers:
  - name: dummy
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
reviewers:
  - name: dummy
    impl: dummy
`,
			want: "default_budget",
		},
		{
			name: "invalid max findings",
			body: `
log_destination: ./reviews
max_findings: 0
reviewers:
  - name: dummy
    impl: dummy
`,
			want: "max_findings",
		},
		{
			name: "duplicate reviewer",
			body: `
log_destination: ./reviews
reviewers:
  - name: dummy
    impl: dummy
  - name: dummy
    impl: dummy
`,
			want: "duplicate reviewer",
		},
		{
			name: "unknown impl",
			body: `
log_destination: ./reviews
reviewers:
  - name: nope
    impl: nope
`,
			want: "unknown impl",
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
reviewers:
  - name: dummy
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
reviewers:
  - name: dummy
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

func writeConfig(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

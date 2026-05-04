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
name: test-project
log_destination: ./reviews
prompt_overrides: |
  flag unclear logging.
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
	if cfg.DefaultBudget != DefaultBudget {
		t.Fatalf("default budget = %d, want %d", cfg.DefaultBudget, DefaultBudget)
	}
	if cfg.LogDestination != filepath.Join(dir, "reviews") {
		t.Fatalf("log destination = %s", cfg.LogDestination)
	}
	if _, err := os.Stat(cfg.LogDestination); err != nil {
		t.Fatalf("expected log destination to be created: %v", err)
	}
	if cfg.Reviewers[0].BinaryPath != filepath.Join(dir, "bin", "codex") {
		t.Fatalf("binary path = %s", cfg.Reviewers[0].BinaryPath)
	}
	if cfg.Reviewers[0].ExtraArgs[0] != "--ignore-user-config" {
		t.Fatalf("extra args not loaded: %+v", cfg.Reviewers[0].ExtraArgs)
	}
}

func TestLoadValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing name",
			body: `
log_destination: ./reviews
reviewers:
  - name: dummy
    impl: dummy
`,
			want: "name is required",
		},
		{
			name: "invalid budget",
			body: `
name: test
log_destination: ./reviews
default_budget: -1
reviewers:
  - name: dummy
    impl: dummy
`,
			want: "default_budget",
		},
		{
			name: "duplicate reviewer",
			body: `
name: test
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
name: test
log_destination: ./reviews
reviewers:
  - name: nope
    impl: nope
`,
			want: "unknown impl",
		},
		{
			name: "missing parent",
			body: `
name: test
log_destination: ./missing/reviews
reviewers:
  - name: dummy
    impl: dummy
`,
			want: "parent",
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

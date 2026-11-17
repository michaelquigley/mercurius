package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveProjectDefaultsAndPaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, cfgPath, `
log_destination: ./reviews
review_focus: |
  flag unclear logging.
review_context: |
  deployment: personal one-shot
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
	if !strings.Contains(cfg.ReviewContext, "deployment: personal one-shot") {
		t.Fatalf("review context = %q", cfg.ReviewContext)
	}
	if !strings.Contains(cfg.ReviewFocus, "flag unclear logging.") {
		t.Fatalf("review focus = %q", cfg.ReviewFocus)
	}
}

// the reviewer comes from the user-level file when the project has no
// override.
func TestResolveUserFileOnly(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeProject(t, dir, "log_destination: ./reviews\n")
	userPath := writeUser(t, dir, `
reviewer:
  name: codex
  impl: codex
  model: gpt-test
  extra_args:
    - --ignore-user-config
`)
	resolved, err := Resolve(cfgPath, userPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	r := resolved.Reviewer
	if r == nil || r.Name != "codex" || r.Impl != "codex" || r.Model != "gpt-test" || r.ExtraArgs[0] != "--ignore-user-config" {
		t.Fatalf("reviewer = %+v", r)
	}
	if r.Timeout != DefaultReviewerTimeout {
		t.Fatalf("timeout = %s, want the default %s", r.Timeout, DefaultReviewerTimeout)
	}
	if resolved.ReviewerSource != SourceUserConfig {
		t.Fatalf("source = %q, want %q", resolved.ReviewerSource, SourceUserConfig)
	}
}

// a project override with no user-level file supplies the reviewer by itself.
func TestResolveProjectOverrideOnly(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeProject(t, dir, "reviewer_override:\n  name: second-opinion\n  impl: claude\n  model: sonnet\n")
	resolved, err := Resolve(cfgPath, filepath.Join(dir, "missing", "config.yaml"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r := resolved.Reviewer; r == nil || r.Name != "second-opinion" || r.Impl != "claude" || r.Model != "sonnet" {
		t.Fatalf("reviewer = %+v", r)
	}
	if resolved.ReviewerSource != SourceProjectOverride {
		t.Fatalf("source = %q, want %q", resolved.ReviewerSource, SourceProjectOverride)
	}
}

// an override replaces the user-level block whole. Merging field by field would
// let the installation's model land on the override's different impl.
func TestResolveOverrideReplacesUserBlockWhole(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeProject(t, dir, "reviewer_override:\n  name: second-opinion\n  impl: claude\n")
	userPath := writeUser(t, dir, `
reviewer:
  name: codex
  impl: codex
  model: gpt-test
  binary_path: /usr/local/bin/codex
  extra_args: [--flag]
  timeout: 45m
`)
	resolved, err := Resolve(cfgPath, userPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	r := resolved.Reviewer
	if r.Name != "second-opinion" || r.Impl != "claude" {
		t.Fatalf("reviewer = %+v", r)
	}
	if r.Model != "" || r.BinaryPath != "" || len(r.ExtraArgs) != 0 {
		t.Fatalf("the override must not inherit fields from the user block: %+v", r)
	}
	if r.Timeout != DefaultReviewerTimeout {
		t.Fatalf("timeout = %s; the override's own default applies, not the user block's 45m", r.Timeout)
	}
	if resolved.ReviewerSource != SourceProjectOverride {
		t.Fatalf("source = %q", resolved.ReviewerSource)
	}
}

// a plain reviewer block in the project file is refused. The message names the
// user-level path and says to remove the block, and does not mention the
// override key: an agent that wrote the block out of habit should remove it,
// not rename it.
func TestResolveRejectsProjectReviewerBlock(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeProject(t, dir, "review_foucs: a typo\nreviewer:\n  name: codex\n  impl: codex\n")
	userPath := writeUser(t, dir, "reviewer:\n  name: dummy\n  impl: dummy\n")
	_, err := Resolve(cfgPath, userPath)
	want := fmt.Sprintf("config '%s': the reviewer is set per installation in the user-level config '%s', not in the project file; remove the 'reviewer' block from this file", cfgPath, userPath)
	if err == nil || err.Error() != want {
		t.Fatalf("expected '%s', got %v", want, err)
	}
	var fileErr *FileError
	if !errors.As(err, &fileErr) || fileErr.Path != cfgPath {
		t.Fatalf("the error must name the project file as the one to fix: %#v", err)
	}
}

func TestResolveRejectsRenamedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "prompt_overrides",
			body: "prompt_overrides: |\n  stale\n",
			want: "field 'prompt_overrides' has been renamed to 'review_focus'; update the YAML key",
		},
		{
			name: "default_budget",
			body: "default_budget: 4\n",
			want: "field 'default_budget' has been removed; rounds are single-shot and no longer share a budget across a session",
		},
		{
			name: "reviewers",
			body: "reviewers:\n  - name: dummy\n    impl: dummy\n",
			want: "the reviewer is set per installation in the user-level config (no user config directory could be determined on this system), not in the project file; remove the 'reviewers' block from this file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeProject(t, t.TempDir(), test.body)
			_, err := Resolve(path, "")
			want := fmt.Sprintf("config '%s': %s", path, test.want)
			if err == nil || err.Error() != want {
				t.Fatalf("expected '%s', got %v", want, err)
			}
			var fileErr *FileError
			if !errors.As(err, &fileErr) || fileErr.Path != path {
				t.Fatalf("the error must name the project file as the one to fix: %#v", err)
			}
		})
	}
}

func TestResolveConfiguresMaxFindings(t *testing.T) {
	resolved, err := Resolve(writeProject(t, t.TempDir(), "max_findings: 9\n"), "")
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if resolved.Project.MaxFindings != 9 {
		t.Fatalf("max findings = %d, want 9", resolved.Project.MaxFindings)
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
			dir := t.TempDir()
			body := "reviewer:\n  name: " + test.impl + "\n  impl: " + test.impl + "\n"
			if test.model != "" {
				body += "  model: " + test.model + "\n"
			}
			resolved, err := Resolve(writeProject(t, dir, ""), writeUser(t, dir, body))
			if err != nil {
				t.Fatalf("resolve config: %v", err)
			}
			if resolved.Reviewer.Impl != test.impl || resolved.Reviewer.Model != test.model {
				t.Fatalf("reviewer = %+v", resolved.Reviewer)
			}
		})
	}
}

func TestResolveValidatesConfig(t *testing.T) {
	tests := []struct {
		name     string
		project  string
		user     string
		want     string
		wantFile string
	}{
		{name: "invalid max findings", project: "max_findings: 0\n", want: "max_findings", wantFile: "project"},
		{name: "unknown impl in the user file", user: "reviewer:\n  name: nope\n  impl: nope\n", want: "unknown impl", wantFile: "user"},
		{name: "negative timeout in the user file", user: "reviewer:\n  name: dummy\n  impl: dummy\n  timeout: -1m\n", want: "timeout", wantFile: "user"},
		{name: "unknown impl in the override", project: "reviewer_override:\n  name: nope\n  impl: nope\n", want: "reviewer_override", wantFile: "project"},
		{name: "unparseable user file", user: "this: is: not: valid: yaml\n\t::broken", want: "user config", wantFile: "user"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := writeProject(t, dir, test.project)
			userPath := writeUser(t, dir, test.user)
			_, err := Resolve(cfgPath, userPath)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q in error, got %v", test.want, err)
			}
			want := cfgPath
			if test.wantFile == "user" {
				want = userPath
			}
			var fileErr *FileError
			if !errors.As(err, &fileErr) || fileErr.Path != want {
				t.Fatalf("the error must name '%s': %#v", want, err)
			}
		})
	}
}

// a relative binary_path resolves against the directory of the file that
// declared it, in each file.
func TestResolveRelativeBinaryPathPerFile(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	userDir := filepath.Join(root, "home", ".config", "mercurius")
	for _, dir := range []string{projectDir, userDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	userPath := filepath.Join(userDir, "config.yaml")
	writeConfig(t, userPath, "reviewer:\n  name: codex\n  impl: codex\n  binary_path: ./bin/codex\n")

	cfgPath := writeProject(t, projectDir, "")
	resolved, err := Resolve(cfgPath, userPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got, want := resolved.Reviewer.BinaryPath, filepath.Join(userDir, "bin", "codex"); got != want {
		t.Fatalf("user binary path = %q, want %q", got, want)
	}

	writeConfig(t, cfgPath, "reviewer_override:\n  name: claude\n  impl: claude\n  binary_path: tools/claude\n")
	resolved, err = Resolve(cfgPath, userPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got, want := resolved.Reviewer.BinaryPath, filepath.Join(projectDir, "tools", "claude"); got != want {
		t.Fatalf("override binary path = %q, want %q", got, want)
	}
}

// with no reviewer in either file, Resolve succeeds and reports none; only
// start_review_round turns the absence into an error.
func TestResolveWithoutReviewer(t *testing.T) {
	dir := t.TempDir()
	for _, userPath := range []string{"", filepath.Join(dir, "missing.yaml"), writeUser(t, dir, "# no reviewer here\n")} {
		resolved, err := Resolve(writeProject(t, dir, "log_destination: ./reviews\n"), userPath)
		if err != nil {
			t.Fatalf("resolve with user path %q: %v", userPath, err)
		}
		if resolved.Reviewer != nil || resolved.ReviewerSource != "" {
			t.Fatalf("reviewer = %+v source = %q, want none", resolved.Reviewer, resolved.ReviewerSource)
		}
	}
}

func TestResolveParsesReviewerTimeout(t *testing.T) {
	dir := t.TempDir()
	resolved, err := Resolve(writeProject(t, dir, ""), writeUser(t, dir, "reviewer:\n  name: dummy\n  impl: dummy\n  timeout: 7m30s\n"))
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

func TestEnsureLogDestinationRejectsMissingParent(t *testing.T) {
	err := EnsureLogDestination(filepath.Join(t.TempDir(), "missing", "reviews"))
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
	resolved, err := Resolve(writeProject(t, t.TempDir(), `
settled_decisions:
  - id: recall-deferred
    do_not_flag: >
      the absence of a 'recall' concept, or suggestions to add it now
  - id: observability-out-of-scope
    do_not_flag: missing production-grade observability
`), "")
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
	// an empty do_not_flag and duplicate ids must not fail the load: a guard
	// can be left blank, and its id is an operator-side handle only.
	resolved, err := Resolve(writeProject(t, t.TempDir(), `
settled_decisions:
  - id: dupe
    do_not_flag: first
  - id: dupe
    do_not_flag: ""
  - id: blank
`), "")
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if len(resolved.Project.SettledDecisions) != 3 {
		t.Fatalf("settled decisions = %d, want 3 (all tolerated)", len(resolved.Project.SettledDecisions))
	}
}

func TestResolveReturnsExactBytes(t *testing.T) {
	body := `log_destination: ./reviews
settled_decisions:
  - id: recall-deferred
    do_not_flag: the absence of a 'recall' concept
`
	resolved, err := Resolve(writeProject(t, t.TempDir(), body), "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(resolved.Raw) != body {
		t.Fatalf("raw bytes do not match file content:\n--- got ---\n%s\n--- want ---\n%s", resolved.Raw, body)
	}
}

func writeProject(t *testing.T, dir string, body string) string {
	t.Helper()
	path := filepath.Join(dir, "mercurius.yaml")
	writeConfig(t, path, body)
	return path
}

// writeUser writes a user-level config in the test's own directory. No test
// reads the developer's real user-level file.
func writeUser(t *testing.T, dir string, body string) string {
	t.Helper()
	path := filepath.Join(dir, "user-config.yaml")
	writeConfig(t, path, body)
	return path
}

func writeConfig(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/michaelquigley/df/dd"
	"gopkg.in/yaml.v3"
)

const (
	DefaultMaxFindings    = 6
	DefaultLogDestination = ".mercurius"
)

var knownImpls = map[string]struct{}{
	"codex": {},
	"dummy": {},
}

// Config is the resolved Mercurius project configuration.
type Config struct {
	Name           string
	ConfigPath     string
	LogDestination string
	MaxFindings    int
	ReviewContext  string
	ReviewFocus    string
	Reviewer       *ReviewerConfig
}

// ReviewerConfig configures one named reviewer.
type ReviewerConfig struct {
	Name       string
	Impl       string
	BinaryPath string
	Model      string
	ExtraArgs  []string
}

// Load reads, resolves, and validates a Mercurius YAML config.
func Load(path string) (*Config, error) {
	if path == "" {
		path = "./mercurius.yaml"
	}

	absPath, err := filepath.Abs(expandHome(path))
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		MaxFindings:    DefaultMaxFindings,
		LogDestination: DefaultLogDestination,
	}
	if err := dd.MergeYAMLFile(cfg, absPath); err != nil {
		return nil, err
	}
	if err := checkRenamedFields(absPath); err != nil {
		return nil, err
	}
	cfg.Name = filepath.Base(filepath.Dir(absPath))

	if err := cfg.resolve(filepath.Dir(absPath)); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.ConfigPath = absPath
	return cfg, nil
}

// Validate checks required fields. It performs pure field validation only and
// has no filesystem side effects, so callers (preview, monitor) that do not
// intend to write can rely on Load() being side-effect-free. Callers that do
// write to log_destination (the MCP server startup path) invoke
// EnsureLogDestination() separately after Load().
func (c *Config) Validate() error {
	if c.MaxFindings <= 0 {
		return fmt.Errorf("max_findings must be greater than zero")
	}
	if c.Reviewer == nil {
		return errors.New("reviewer is required")
	}
	if c.Reviewer.Name == "" {
		return errors.New("reviewer name is required")
	}
	if c.Reviewer.Impl == "" {
		return fmt.Errorf("reviewer '%s': impl is required", c.Reviewer.Name)
	}
	if _, ok := knownImpls[c.Reviewer.Impl]; !ok {
		return fmt.Errorf("reviewer '%s': unknown impl '%s' (known: %s)", c.Reviewer.Name, c.Reviewer.Impl, strings.Join(KnownImpls(), ", "))
	}
	return nil
}

// EnsureLogDestination creates the configured log_destination directory if it
// does not already exist and verifies it is writable. Callers that intend to
// write rounds, status files, or events to the directory must invoke this
// before opening a session.
func (c *Config) EnsureLogDestination() error {
	if err := ensureLogDestination(c.LogDestination); err != nil {
		return fmt.Errorf("log_destination: %w", err)
	}
	return nil
}

// checkRenamedFields rejects configs that still use renamed YAML keys. dd
// silently drops unknown fields, so a stale key would otherwise produce
// confusing review behavior with no error.
func checkRenamedFields(absPath string) error {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	var generic map[string]any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		// dd already validated the YAML; ignore parse errors here so this
		// guard does not produce a different error than the primary load.
		return nil
	}
	if _, ok := generic["prompt_overrides"]; ok {
		return fmt.Errorf("config '%s': field 'prompt_overrides' has been renamed to 'review_focus'; update the YAML key", absPath)
	}
	if _, ok := generic["default_budget"]; ok {
		return fmt.Errorf("config '%s': field 'default_budget' has been removed; rounds are single-shot and no longer share a budget across a session", absPath)
	}
	if _, ok := generic["reviewers"]; ok {
		return fmt.Errorf("config '%s': field 'reviewers' has been renamed to 'reviewer' (singular); update the YAML key", absPath)
	}
	return nil
}

// KnownImpls returns the reviewer implementations registered in this binary.
func KnownImpls() []string {
	impls := make([]string, 0, len(knownImpls))
	for impl := range knownImpls {
		impls = append(impls, impl)
	}
	slices.Sort(impls)
	return impls
}

func (c *Config) resolve(baseDir string) error {
	c.LogDestination = resolvePath(baseDir, c.LogDestination)
	if c.Reviewer != nil && c.Reviewer.BinaryPath != "" {
		c.Reviewer.BinaryPath = resolvePath(baseDir, c.Reviewer.BinaryPath)
	}
	return nil
}

func resolvePath(baseDir string, path string) string {
	if path == "" {
		return path
	}
	path = expandHome(path)
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func ensureLogDestination(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("'%s' is not a directory", path)
		}
		return checkWritable(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	parent := filepath.Dir(path)
	info, err = os.Stat(parent)
	if err != nil {
		return fmt.Errorf("parent '%s' is not available: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("parent '%s' is not a directory", parent)
	}
	if err := checkWritable(parent); err != nil {
		return err
	}
	return os.Mkdir(path, 0o700)
}

func checkWritable(dir string) error {
	file, err := os.CreateTemp(dir, ".mercurius-write-*")
	if err != nil {
		return fmt.Errorf("directory '%s' is not writable: %w", dir, err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

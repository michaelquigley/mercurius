package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/michaelquigley/df/dd"
)

const (
	DefaultBudget      = 4
	DefaultMaxFindings = 10
)

var knownImpls = map[string]struct{}{
	"codex": {},
	"dummy": {},
}

// Config is the resolved Mercurius project configuration.
type Config struct {
	Name            string
	ConfigPath      string
	LogDestination  string
	DefaultBudget   int
	MaxFindings     int
	ReviewContext   string
	PromptOverrides string
	Reviewers       []*ReviewerConfig
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
		DefaultBudget: DefaultBudget,
		MaxFindings:   DefaultMaxFindings,
	}
	if err := dd.MergeYAMLFile(cfg, absPath); err != nil {
		return nil, err
	}

	if err := cfg.resolve(filepath.Dir(absPath)); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.ConfigPath = absPath
	return cfg, nil
}

// Validate checks required fields and path writability.
func (c *Config) Validate() error {
	if c.Name == "" {
		return errors.New("name is required")
	}
	if c.LogDestination == "" {
		return errors.New("log_destination is required")
	}
	if c.DefaultBudget <= 0 {
		return fmt.Errorf("default_budget must be greater than zero")
	}
	if c.MaxFindings <= 0 {
		return fmt.Errorf("max_findings must be greater than zero")
	}
	if len(c.Reviewers) == 0 {
		return errors.New("reviewers is required")
	}

	seen := map[string]struct{}{}
	for _, reviewer := range c.Reviewers {
		if reviewer == nil {
			return errors.New("reviewer entry is nil")
		}
		if reviewer.Name == "" {
			return errors.New("reviewer name is required")
		}
		if _, ok := seen[reviewer.Name]; ok {
			return fmt.Errorf("duplicate reviewer name '%s'", reviewer.Name)
		}
		seen[reviewer.Name] = struct{}{}
		if reviewer.Impl == "" {
			return fmt.Errorf("reviewer '%s': impl is required", reviewer.Name)
		}
		if _, ok := knownImpls[reviewer.Impl]; !ok {
			return fmt.Errorf("reviewer '%s': unknown impl '%s' (known: %s)", reviewer.Name, reviewer.Impl, strings.Join(KnownImpls(), ", "))
		}
	}
	if err := ensureLogDestination(c.LogDestination); err != nil {
		return fmt.Errorf("log_destination: %w", err)
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
	for _, reviewer := range c.Reviewers {
		if reviewer == nil {
			continue
		}
		if reviewer.BinaryPath != "" {
			reviewer.BinaryPath = resolvePath(baseDir, reviewer.BinaryPath)
		}
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

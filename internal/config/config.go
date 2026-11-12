package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/michaelquigley/df/dd"
	"gopkg.in/yaml.v3"
)

const (
	DefaultMaxFindings    = 6
	DefaultLogDestination = ".mercurius"
	// DefaultReviewerTimeout bounds a round when the reviewer block names no
	// timeout. it errs long on purpose: a timeout that fires early throws away a
	// good review, while one that fires late only holds the runner lock longer.
	DefaultReviewerTimeout = 20 * time.Minute
	// DefaultProjectPath is where the project config is looked for when no
	// --config flag is given.
	DefaultProjectPath = "./mercurius.yaml"
)

var knownImpls = map[string]struct{}{
	"codex":  {},
	"claude": {},
	"pi":     {},
	"dummy":  {},
}

// Config is the resolved project configuration: the settings that describe the
// project and the review arc. The reviewer is resolved separately; see Resolved.
type Config struct {
	Name             string
	ConfigPath       string
	LogDestination   string
	MaxFindings      int
	ReviewContext    string
	ReviewFocus      string
	SettledDecisions []SettledDecision
}

// SettledDecision is one operator-side guard: a decision already made that the
// reviewer should stop re-raising. DoNotFlag is the load-bearing instruction
// rendered into the round prompt; ID is an operator-side handle for finding and
// editing the guard and never reaches the reviewer. dd auto-converts the field
// names to 'id' and 'do_not_flag', so no struct tags are needed.
type SettledDecision struct {
	ID        string
	DoNotFlag string
}

// ReviewerConfig configures the reviewer. Timeout bounds one round; zero means
// DefaultReviewerTimeout. dd reads it from strings like '20m'.
type ReviewerConfig struct {
	Name       string
	Impl       string
	BinaryPath string
	Model      string
	ExtraArgs  []string
	Timeout    time.Duration
}

// Resolved is the result of Resolve: the project settings, the reviewer the next
// round would run with (nil when none is configured), and the project file's
// exact bytes, which a round snapshots as _config.yaml.
type Resolved struct {
	Project  Config
	Reviewer *ReviewerConfig
	Raw      []byte
}

// projectFile is the YAML shape of mercurius.yaml.
type projectFile struct {
	LogDestination   string
	MaxFindings      int
	ReviewContext    string
	ReviewFocus      string
	SettledDecisions []SettledDecision
	Reviewer         *ReviewerConfig
}

// ProjectPath returns the absolute project config path for a --config value,
// expanding '~'. It does not read the file.
func ProjectPath(path string) (string, error) {
	if path == "" {
		path = DefaultProjectPath
	}
	return filepath.Abs(expandHome(path))
}

// ProjectName returns the project name for a project config path: the base name
// of the directory holding it. It does not read the file.
func ProjectName(projectPath string) string {
	return filepath.Base(filepath.Dir(projectPath))
}

// UserConfigPath returns the user-level config path,
// '<os.UserConfigDir()>/mercurius/config.yaml', or an empty string when no user
// config directory can be determined.
func UserConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mercurius", "config.yaml")
}

// Resolve reads and validates the configuration a broker operation runs with.
// It is called by every operation, so edits take effect on the next call. The
// project file is read exactly once; the returned bytes are the ones parsed. A
// missing or unparseable project file is an error; a missing reviewer is not,
// and is reported as a nil Reviewer for the caller to judge.
//
// userPath is accepted for the user-level reviewer config and is not read yet;
// the reviewer still comes from the project file's 'reviewer' block.
func Resolve(projectPath string, userPath string) (*Resolved, error) {
	_ = userPath

	absPath, err := ProjectPath(projectPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config '%s' not found: %w", absPath, err)
		}
		return nil, fmt.Errorf("read config '%s': %w", absPath, err)
	}

	file := &projectFile{
		MaxFindings:    DefaultMaxFindings,
		LogDestination: DefaultLogDestination,
	}
	if err := dd.MergeYAML(file, raw); err != nil {
		return nil, fmt.Errorf("config '%s': %w", absPath, err)
	}
	if err := checkRenamedFields(absPath, raw); err != nil {
		return nil, err
	}

	baseDir := filepath.Dir(absPath)
	resolved := &Resolved{
		Project: Config{
			Name:             ProjectName(absPath),
			ConfigPath:       absPath,
			LogDestination:   resolvePath(baseDir, file.LogDestination),
			MaxFindings:      file.MaxFindings,
			ReviewContext:    file.ReviewContext,
			ReviewFocus:      file.ReviewFocus,
			SettledDecisions: file.SettledDecisions,
		},
		Raw: raw,
	}
	if file.Reviewer != nil {
		reviewerCfg := *file.Reviewer
		reviewerCfg.ExtraArgs = append([]string(nil), file.Reviewer.ExtraArgs...)
		if reviewerCfg.BinaryPath != "" {
			reviewerCfg.BinaryPath = resolvePath(baseDir, reviewerCfg.BinaryPath)
		}
		if reviewerCfg.Timeout == 0 {
			reviewerCfg.Timeout = DefaultReviewerTimeout
		}
		resolved.Reviewer = &reviewerCfg
	}

	if err := resolved.Project.Validate(); err != nil {
		return nil, fmt.Errorf("config '%s': %w", absPath, err)
	}
	if resolved.Reviewer != nil {
		if err := resolved.Reviewer.Validate(); err != nil {
			return nil, fmt.Errorf("config '%s': %w", absPath, err)
		}
	}
	return resolved, nil
}

// Validate checks the project settings. It performs pure field validation and
// has no filesystem side effects.
func (c *Config) Validate() error {
	if c.MaxFindings <= 0 {
		return fmt.Errorf("max_findings must be greater than zero")
	}
	return nil
}

// Validate checks a reviewer block.
func (r *ReviewerConfig) Validate() error {
	if r.Name == "" {
		return errors.New("reviewer name is required")
	}
	if r.Impl == "" {
		return fmt.Errorf("reviewer '%s': impl is required", r.Name)
	}
	if _, ok := knownImpls[r.Impl]; !ok {
		return fmt.Errorf("reviewer '%s': unknown impl '%s' (known: %s)", r.Name, r.Impl, strings.Join(KnownImpls(), ", "))
	}
	if r.Timeout < 0 {
		return fmt.Errorf("reviewer '%s': timeout must not be negative", r.Name)
	}
	return nil
}

// EnsureLogDestination creates the log_destination directory if it does not
// already exist and verifies it is writable. Only open_session calls it; every
// other operation reads a log destination a session already lives in.
func EnsureLogDestination(path string) error {
	if path == "" {
		return errors.New("log_destination: is empty")
	}
	if err := ensureLogDestination(path); err != nil {
		return fmt.Errorf("log_destination: %w", err)
	}
	return nil
}

// checkRenamedFields rejects configs that still use renamed YAML keys. dd
// silently drops unknown fields, so a stale key would otherwise produce
// confusing review behavior with no error. It works from the already-read
// bytes so the config file is not read a second time. absPath is used only for
// error messages.
func checkRenamedFields(absPath string, raw []byte) error {
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

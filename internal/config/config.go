package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/michaelquigley/df/dd"
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
// editing the guard and never reaches the reviewer.
type SettledDecision struct {
	ID        string
	DoNotFlag string
	Extra     map[string]any `dd:",+extra"`
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
	Extra      map[string]any `dd:",+extra"`
}

// where a resolved reviewer came from.
const (
	SourceUserConfig      = "user config"
	SourceProjectOverride = "project override"
)

// Resolved is the result of Resolve: the project settings, the reviewer the next
// round would run with (nil when none is configured) and where it came from,
// and the project file's exact bytes, which a round snapshots as _config.yaml.
type Resolved struct {
	Project        Config
	Reviewer       *ReviewerConfig
	ReviewerSource string
	Raw            []byte
}

// FileError is a configuration file that could not be read, parsed, or
// validated. Path names the file, so the caller can say which of the two
// files to fix.
type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string {
	return e.Err.Error()
}

func (e *FileError) Unwrap() error {
	return e.Err
}

// projectFile is the YAML shape of the project's mercurius.yaml. The reviewer
// is not part of it: a project chooses its own reviewer only through
// reviewer_override, and a plain reviewer block is rejected.
type projectFile struct {
	LogDestination   string
	MaxFindings      int
	ReviewContext    string
	ReviewFocus      string
	SettledDecisions []SettledDecision
	ReviewerOverride *ReviewerConfig
	Extra            map[string]any `dd:",+extra"`
}

// userFile is the YAML shape of the user-level config. The reviewer is a fact
// about the installation, set once here.
type userFile struct {
	Reviewer *ReviewerConfig
	Extra    map[string]any `dd:",+extra"`
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
// It is called by every operation, so edits to either file take effect on the
// next call. Each file is read and parsed once. The returned project bytes
// are the ones parsed.
//
// The reviewer comes from the user-level file's reviewer block, unless the
// project file has a reviewer_override block, which replaces it whole, timeout
// included; the two are never merged field by field. A missing user-level file
// is no reviewer, not an error. A missing reviewer is not an error either, and
// is reported as a nil Reviewer for the caller to judge. Errors are FileErrors
// naming the file at fault.
func Resolve(projectPath string, userPath string) (*Resolved, error) {
	absPath, err := ProjectPath(projectPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &FileError{Path: absPath, Err: fmt.Errorf("config '%s' not found: %w", absPath, err)}
		}
		return nil, &FileError{Path: absPath, Err: fmt.Errorf("read config '%s': %w", absPath, err)}
	}

	file := &projectFile{
		MaxFindings:    DefaultMaxFindings,
		LogDestination: DefaultLogDestination,
	}
	if err := dd.MergeYAML(file, raw); err != nil {
		return nil, &FileError{Path: absPath, Err: fmt.Errorf("config '%s': %w", absPath, err)}
	}
	if err := checkProjectKeys(absPath, userPath, file.Extra); err != nil {
		return nil, &FileError{Path: absPath, Err: err}
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
	if err := resolved.Project.Validate(); err != nil {
		return nil, &FileError{Path: absPath, Err: fmt.Errorf("config '%s': %w", absPath, err)}
	}

	userReviewer, err := readUserReviewer(userPath)
	if err != nil {
		return nil, err
	}
	switch {
	case file.ReviewerOverride != nil:
		if err := rejectUnknownKeys(file.ReviewerOverride.Extra); err != nil {
			return nil, &FileError{Path: absPath, Err: fmt.Errorf("config '%s': reviewer_override: %w", absPath, err)}
		}
		reviewerCfg, err := resolveReviewer(*file.ReviewerOverride, baseDir)
		if err != nil {
			return nil, &FileError{Path: absPath, Err: fmt.Errorf("config '%s': reviewer_override: %w", absPath, err)}
		}
		resolved.Reviewer = reviewerCfg
		resolved.ReviewerSource = SourceProjectOverride
	case userReviewer != nil:
		resolved.Reviewer = userReviewer
		resolved.ReviewerSource = SourceUserConfig
	}
	return resolved, nil
}

// readUserReviewer reads the reviewer block from the user-level file. An empty
// path, a missing file, or a file with no reviewer block is no reviewer.
func readUserReviewer(userPath string) (*ReviewerConfig, error) {
	if userPath == "" {
		return nil, nil
	}
	absPath, err := filepath.Abs(expandHome(userPath))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(absPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, &FileError{Path: absPath, Err: fmt.Errorf("read user config '%s': %w", absPath, err)}
	}
	file := &userFile{}
	if err := dd.MergeYAML(file, raw); err != nil {
		return nil, &FileError{Path: absPath, Err: fmt.Errorf("user config '%s': %w", absPath, err)}
	}
	if err := rejectUnknownKeys(file.Extra); err != nil {
		return nil, &FileError{Path: absPath, Err: fmt.Errorf("user config '%s': %w", absPath, err)}
	}
	if file.Reviewer == nil {
		return nil, nil
	}
	if err := rejectUnknownKeys(file.Reviewer.Extra); err != nil {
		return nil, &FileError{Path: absPath, Err: fmt.Errorf("user config '%s': reviewer: %w", absPath, err)}
	}
	reviewerCfg, err := resolveReviewer(*file.Reviewer, filepath.Dir(absPath))
	if err != nil {
		return nil, &FileError{Path: absPath, Err: fmt.Errorf("user config '%s': %w", absPath, err)}
	}
	return reviewerCfg, nil
}

// resolveReviewer resolves a reviewer block against the directory of the file
// that declared it, applies the default timeout, and validates it.
func resolveReviewer(block ReviewerConfig, baseDir string) (*ReviewerConfig, error) {
	reviewerCfg := block
	reviewerCfg.ExtraArgs = append([]string(nil), block.ExtraArgs...)
	if reviewerCfg.BinaryPath != "" {
		reviewerCfg.BinaryPath = resolvePath(baseDir, reviewerCfg.BinaryPath)
	}
	if reviewerCfg.Timeout == 0 {
		reviewerCfg.Timeout = DefaultReviewerTimeout
	}
	if err := reviewerCfg.Validate(); err != nil {
		return nil, err
	}
	return &reviewerCfg, nil
}

// Validate checks the project settings. It performs pure field validation and
// has no filesystem side effects.
func (c *Config) Validate() error {
	if c.MaxFindings <= 0 {
		return fmt.Errorf("max_findings must be greater than zero")
	}
	for i, decision := range c.SettledDecisions {
		if err := rejectUnknownKeys(decision.Extra); err != nil {
			return fmt.Errorf("settled_decisions[%d]: %w", i, err)
		}
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

// checkProjectKeys reports stale keys before other unknown keys so a project
// with an old config still gets its migration instructions.
//
// A plain reviewer block is rejected with a message that names the user-level
// file and says to remove the block. It deliberately does not mention
// reviewer_override: an agent that wrote the block out of habit should remove
// it, not rename it into an override nobody decided on.
func checkProjectKeys(absPath string, userPath string, extra map[string]any) error {
	if _, ok := extra["prompt_overrides"]; ok {
		return fmt.Errorf("config '%s': field 'prompt_overrides' has been renamed to 'review_focus'; update the YAML key", absPath)
	}
	if _, ok := extra["default_budget"]; ok {
		return fmt.Errorf("config '%s': field 'default_budget' has been removed; rounds are single-shot and no longer share a budget across a session", absPath)
	}
	if _, ok := extra["reviewer"]; ok {
		return fmt.Errorf("config '%s': the reviewer is set per installation in the user-level config %s, not in the project file; remove the 'reviewer' block from this file", absPath, describeUserPath(userPath))
	}
	if _, ok := extra["reviewers"]; ok {
		return fmt.Errorf("config '%s': the reviewer is set per installation in the user-level config %s, not in the project file; remove the 'reviewers' block from this file", absPath, describeUserPath(userPath))
	}
	if err := rejectUnknownKeys(extra); err != nil {
		return fmt.Errorf("config '%s': %w", absPath, err)
	}
	return nil
}

// rejectUnknownKeys reports the first unknown key in lexical order. dd
// captures keys in each bound object's own +extra field.
func rejectUnknownKeys(extra map[string]any) error {
	if len(extra) == 0 {
		return nil
	}
	keys := slices.Sorted(maps.Keys(extra))
	return fmt.Errorf("unknown key '%s'", keys[0])
}

// describeUserPath quotes the user-level config path for a message, or says
// that it could not be determined.
func describeUserPath(userPath string) string {
	if userPath == "" {
		return "(no user config directory could be determined on this system)"
	}
	return fmt.Sprintf("'%s'", userPath)
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

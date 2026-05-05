package monitor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	StatusFileName = "status.json"
	EventsFileName = "events.ndjson"
)

type ReviewerInfo struct {
	Name  string `json:"name"`
	Impl  string `json:"impl"`
	Model string `json:"model,omitempty"`
}

type RegisteredArtifact struct {
	Name       string `json:"name"`
	SourcePath string `json:"source_path"`
	Inline     bool   `json:"inline"`
}

type ErrorInfo struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details"`
	Retryable  bool           `json:"retryable"`
	NextAction string         `json:"next_action"`
	At         time.Time      `json:"at,omitempty"`
}

type RoundJob struct {
	SessionID   string     `json:"session_id"`
	RoundNumber int        `json:"round_number"`
	State       string     `json:"state"`
	Reviewer    string     `json:"reviewer"`
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	LogPath     string     `json:"log_path,omitempty"`
	StatusPath  string     `json:"status_path"`
	EventsPath  string     `json:"events_path"`
	Error       *ErrorInfo `json:"error,omitempty"`
}

type RoundStatus struct {
	RoundNumber   int       `json:"round_number"`
	OpenedAt      time.Time `json:"opened_at"`
	LogPath       string    `json:"log_path"`
	HasNotes      bool      `json:"has_notes"`
	DecisionCount int       `json:"decision_count"`
}

type Convergence struct {
	Signal                      string `json:"signal"`
	Message                     string `json:"message"`
	LatestBlockingFindings      int    `json:"latest_blocking_findings"`
	PreviousBlockingFindings    int    `json:"previous_blocking_findings"`
	DeclinedOrDeferredDecisions int    `json:"declined_or_deferred_decisions"`
	AcceptedDecisions           int    `json:"accepted_decisions"`
}

type SessionStatus struct {
	SessionID            string               `json:"session_id"`
	State                string               `json:"state"`
	Verdict              *string              `json:"verdict"`
	OpenedAt             time.Time            `json:"opened_at"`
	ClosedAt             *time.Time           `json:"closed_at,omitempty"`
	Budget               int                  `json:"budget"`
	BudgetRemaining      int                  `json:"budget_remaining"`
	MaxFindings          int                  `json:"max_findings"`
	ReviewContextSource  string               `json:"review_context_source"`
	ReviewContextPresent bool                 `json:"review_context_present"`
	RoundsUsed           int                  `json:"rounds_used"`
	Reviewers            []ReviewerInfo       `json:"reviewers"`
	Artifacts            []RegisteredArtifact `json:"artifacts"`
	LastError            *ErrorInfo           `json:"last_error,omitempty"`
	ActiveRound          *RoundJob            `json:"active_round,omitempty"`
	LastRoundJob         *RoundJob            `json:"last_round_job,omitempty"`
	Rounds               []RoundStatus        `json:"rounds"`
	Convergence          Convergence          `json:"convergence"`
}

type Event struct {
	At          time.Time  `json:"at"`
	Event       string     `json:"event"`
	SessionID   string     `json:"session_id"`
	RoundNumber int        `json:"round_number,omitempty"`
	Reviewer    string     `json:"reviewer,omitempty"`
	State       string     `json:"state,omitempty"`
	LogPath     string     `json:"log_path,omitempty"`
	Error       *ErrorInfo `json:"error,omitempty"`
}

func SessionDir(logDestination string, sessionID string) string {
	return filepath.Join(logDestination, sessionID)
}

func StatusPath(sessionDir string) string {
	return filepath.Join(sessionDir, StatusFileName)
}

func EventsPath(sessionDir string) string {
	return filepath.Join(sessionDir, EventsFileName)
}

func WriteStatus(path string, status SessionStatus) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".status-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func AppendEvent(path string, event Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}

func ReadStatus(path string) (SessionStatus, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SessionStatus{}, err
	}
	var status SessionStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return SessionStatus{}, err
	}
	return status, nil
}

func ReadEvents(path string) ([]Event, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

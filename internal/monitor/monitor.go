package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const StatusFileName = "status.json"

type ReviewerInfo struct {
	Name  string `json:"name"`
	Impl  string `json:"impl"`
	Model string `json:"model,omitempty"`
}

type ErrorInfo struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
	At      time.Time      `json:"at,omitempty"`
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
	Error       *ErrorInfo `json:"error,omitempty"`
}

type RoundStatus struct {
	RoundNumber   int       `json:"round_number"`
	OpenedAt      time.Time `json:"opened_at"`
	LogPath       string    `json:"log_path"`
	HasNotes      bool      `json:"has_notes"`
	DecisionCount int       `json:"decision_count"`
}

type SessionStatus struct {
	SessionID            string        `json:"session_id"`
	State                string        `json:"state"`
	OpenedAt             time.Time     `json:"opened_at"`
	ClosedAt             *time.Time    `json:"closed_at,omitempty"`
	MaxFindings          int           `json:"max_findings"`
	ReviewContextPresent bool          `json:"review_context_present"`
	ReviewFocusPresent   bool          `json:"review_focus_present"`
	RoundCount           int           `json:"round_count"`
	Reviewer             ReviewerInfo  `json:"reviewer"`
	LastError            *ErrorInfo    `json:"last_error,omitempty"`
	ActiveRound          *RoundJob     `json:"active_round,omitempty"`
	Rounds               []RoundStatus `json:"rounds"`
}

func SessionDir(logDestination string, sessionID string) string {
	return filepath.Join(logDestination, sessionID)
}

func StatusPath(sessionDir string) string {
	return filepath.Join(sessionDir, StatusFileName)
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

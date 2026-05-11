package monitor

import (
	"testing"
	"time"
)

func TestWriteReadStatus(t *testing.T) {
	dir := t.TempDir()
	statusPath := StatusPath(dir)
	now := time.Now().UTC()
	status := SessionStatus{
		SessionID:            "s_test",
		State:                "active",
		OpenedAt:             now,
		MaxFindings:          10,
		ReviewContextPresent: true,
		ReviewFocusPresent:   true,
		RoundCount:           0,
		Reviewer:             ReviewerInfo{Name: "codex", Impl: "codex"},
		ActiveRound: &RoundJob{
			SessionID:   "s_test",
			RoundNumber: 1,
			State:       "running",
			Reviewer:    "codex",
			StartedAt:   now,
			UpdatedAt:   now,
			StatusPath:  statusPath,
		},
	}

	if err := WriteStatus(statusPath, status); err != nil {
		t.Fatalf("write status: %v", err)
	}
	read, err := ReadStatus(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if read.SessionID != "s_test" || read.ActiveRound == nil || read.ActiveRound.RoundNumber != 1 || !read.ReviewContextPresent || !read.ReviewFocusPresent || read.Reviewer.Name != "codex" {
		t.Fatalf("status = %+v", read)
	}
}

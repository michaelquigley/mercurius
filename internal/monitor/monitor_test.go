package monitor

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadStatusAndEvents(t *testing.T) {
	dir := t.TempDir()
	statusPath := StatusPath(dir)
	eventsPath := EventsPath(dir)
	now := time.Now().UTC()
	status := SessionStatus{
		SessionID:            "s_test",
		State:                "active",
		OpenedAt:             now,
		Budget:               2,
		BudgetRemaining:      2,
		MaxFindings:          10,
		ReviewContextSource:  "config",
		ReviewContextPresent: true,
		Convergence: Convergence{
			Signal:                 "watch",
			Message:                "consider closing soon",
			LatestBlockingFindings: 1,
		},
		ActiveRound: &RoundJob{
			SessionID:   "s_test",
			RoundNumber: 1,
			State:       "running",
			Reviewer:    "codex",
			StartedAt:   now,
			UpdatedAt:   now,
			StatusPath:  statusPath,
			EventsPath:  eventsPath,
		},
	}

	if err := WriteStatus(statusPath, status); err != nil {
		t.Fatalf("write status: %v", err)
	}
	read, err := ReadStatus(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if read.SessionID != "s_test" || read.ActiveRound == nil || read.ActiveRound.RoundNumber != 1 || read.ReviewContextSource != "config" || read.Convergence.Signal != "watch" {
		t.Fatalf("status = %+v", read)
	}

	if err := AppendEvent(eventsPath, Event{At: now, Event: "round_started", SessionID: "s_test", RoundNumber: 1}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if err := AppendEvent(eventsPath, Event{At: now, Event: "round_completed", SessionID: "s_test", RoundNumber: 1, LogPath: filepath.Join(dir, "round-01.md")}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	events, err := ReadEvents(eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 2 || events[0].Event != "round_started" || events[1].Event != "round_completed" {
		t.Fatalf("events = %+v", events)
	}
}

package runner

import (
	"testing"

	"github.com/michaelquigley/mercurius/internal/store"
)

func TestBuildReviewerKnownImpls(t *testing.T) {
	for _, impl := range []string{"codex", "claude", "pi", "dummy"} {
		t.Run(impl, func(t *testing.T) {
			r, err := BuildReviewer(store.RunReviewer{Name: impl, Impl: impl, Model: "m"})
			if err != nil {
				t.Fatalf("BuildReviewer(%q): %v", impl, err)
			}
			if r == nil {
				t.Fatalf("BuildReviewer(%q) returned a nil reviewer", impl)
			}
		})
	}
	if _, err := BuildReviewer(store.RunReviewer{Name: "x", Impl: "nope"}); err == nil {
		t.Fatal("expected error for unknown impl")
	}
}

package pi

import (
	"strings"
	"testing"
)

func TestFinalMessage(t *testing.T) {
	userMsg := `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"the prompt"}]}}`
	updateMsg := `{"type":"message_update","message":{"role":"assistant","content":[{"type":"text","text":"partial..."}]}}`
	thinkingOnly := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"}]}}`

	tests := []struct {
		name    string
		lines   []string
		want    string
		wantErr bool
	}{
		{
			name:  "single assistant message",
			lines: []string{userMsg, assistantMessageEnd(`{"ok":true}`)},
			want:  `{"ok":true}`,
		},
		{
			name:  "returns last assistant message",
			lines: []string{assistantMessageEnd(`{"first":true}`), assistantMessageEnd(`{"second":true}`)},
			want:  `{"second":true}`,
		},
		{
			name:  "ignores user and update events",
			lines: []string{userMsg, updateMsg, assistantMessageEnd(`{"answer":1}`)},
			want:  `{"answer":1}`,
		},
		{
			name:  "skips earlier thinking-only assistant message",
			lines: []string{thinkingOnly, assistantMessageEnd(`{"answer":2}`)},
			want:  `{"answer":2}`,
		},
		{
			name:  "tolerates non-json lines",
			lines: []string{"a startup banner", userMsg, assistantMessageEnd(`{"answer":3}`)},
			want:  `{"answer":3}`,
		},
		{
			name:    "no assistant message",
			lines:   []string{userMsg, updateMsg},
			wantErr: true,
		},
		{
			name:    "thinking only is not an answer",
			lines:   []string{thinkingOnly},
			wantErr: true,
		},
		{
			name:    "empty",
			lines:   []string{"  ", ""},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := finalMessage([]byte(strings.Join(test.lines, "\n")))
			if test.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.want {
				t.Fatalf("final message mismatch: got %q want %q", got, test.want)
			}
		})
	}
}

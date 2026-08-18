package pi

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// piEvent is the subset of a pi '--mode json' stream event that mercurius reads.
// the stream is newline-delimited json; each assistant turn ends with a
// 'message_end' event whose message carries the answer in its content blocks.
// verified against pi v0.80.7 - a version bump is the place to re-check these
// field names.
type piEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// finalMessage returns the text of the last assistant message in a pi event
// stream. it concatenates the 'text' content blocks of the final 'message_end'
// assistant message and ignores 'thinking' blocks. lines that are not valid json
// events are tolerated and skipped.
func finalMessage(stdout []byte) (string, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return "", errors.New("pi reviewer produced no output")
	}

	var final string
	found := false
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev piEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "message_end" || ev.Message == nil || ev.Message.Role != "assistant" {
			continue
		}

		var b strings.Builder
		for _, block := range ev.Message.Content {
			if block.Type == "text" {
				b.WriteString(block.Text)
			}
		}
		if text := b.String(); strings.TrimSpace(text) != "" {
			final = text
			found = true
		}
	}

	if !found {
		return "", errors.New("pi reviewer event stream contained no assistant message")
	}
	return final, nil
}

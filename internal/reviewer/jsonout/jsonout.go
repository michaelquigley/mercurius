// Package jsonout extracts a single JSON object from raw reviewer output. A
// reviewer subprocess may emit the object directly, wrapped in a markdown fence,
// or surrounded by stray prose; Object recovers the object in each case. It does
// not validate the object against any schema - the broker performs canonical
// validation after the reviewer returns.
package jsonout

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Object returns the first plausible JSON object found in output. It accepts a
// bare object, a fenced object, or an object embedded in surrounding prose, and
// rejects output that is empty or carries no JSON object (for example a bare
// JSON array).
func Object(output []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return nil, errors.New("reviewer produced no output")
	}

	if isJSONObject(trimmed) {
		return copyRaw(trimmed), nil
	}

	if fenced, ok := stripMarkdownFence(trimmed); ok {
		fenced = bytes.TrimSpace(fenced)
		if isJSONObject(fenced) {
			return copyRaw(fenced), nil
		}
		if json.Valid(fenced) {
			return nil, errors.New("reviewer output does not contain a json object")
		}
	}

	if json.Valid(trimmed) {
		return nil, errors.New("reviewer output does not contain a json object")
	}

	if raw, ok := firstJSONObject(trimmed); ok {
		return raw, nil
	}

	return nil, errors.New("reviewer output does not contain a json object")
}

func isJSONObject(raw []byte) bool {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	if decoder.More() {
		return false
	}
	if _, ok := value.(map[string]any); !ok {
		return false
	}
	return json.Valid(raw)
}

func stripMarkdownFence(raw []byte) ([]byte, bool) {
	lines := bytes.Split(raw, []byte("\n"))
	if len(lines) < 3 {
		return nil, false
	}

	firstLine := strings.TrimSpace(string(lines[0]))
	backticks := leadingBackticks(firstLine)
	if backticks < 3 {
		return nil, false
	}

	lastLine := strings.TrimSpace(string(lines[len(lines)-1]))
	if lastLine != strings.Repeat("`", backticks) {
		return nil, false
	}

	return bytes.Join(lines[1:len(lines)-1], []byte("\n")), true
}

func leadingBackticks(s string) int {
	count := 0
	for _, r := range s {
		if r != '`' {
			break
		}
		count++
	}
	return count
}

func firstJSONObject(raw []byte) (json.RawMessage, bool) {
	for start, b := range raw {
		if b != '{' {
			continue
		}

		decoder := json.NewDecoder(bytes.NewReader(raw[start:]))
		decoder.UseNumber()

		var value any
		if err := decoder.Decode(&value); err != nil {
			continue
		}
		if _, ok := value.(map[string]any); !ok {
			continue
		}

		end := start + int(decoder.InputOffset())
		return copyRaw(bytes.TrimSpace(raw[start:end])), true
	}

	return nil, false
}

func copyRaw(raw []byte) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

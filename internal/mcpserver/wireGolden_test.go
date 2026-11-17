package mcpserver

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var updateWireGoldens = flag.Bool("update", false, "rewrite the selected wire goldens")

type wireNormalizer struct {
	replacements *strings.Replacer
}

func (n wireNormalizer) structured(t *testing.T, result *mcp.CallToolResult) any {
	t.Helper()
	// the client has already decoded structured content through float64.
	return n.value("", decodeWireJSON(t, marshalWireJSON(t, result.StructuredContent)))
}

func (n wireNormalizer) file(t *testing.T, path string) any {
	t.Helper()
	return n.value("", decodeWireJSON(t, []byte(readFileContent(t, path))))
}

func (n wireNormalizer) value(key string, value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, entry := range value {
			value[key] = n.value(key, entry)
		}
	case []any:
		for i, entry := range value {
			value[i] = n.value("", entry)
		}
	case string:
		switch key {
		case "at", "opened_at", "started_at", "completed_at", "updated_at", "closed_at", "recorded_at":
			if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
				return "<time>"
			}
		case "running_for":
			if _, err := time.ParseDuration(value); err == nil {
				return "<elapsed>"
			}
		case "hash":
			if digest, ok := strings.CutPrefix(value, "sha256:"); ok && len(digest) == 64 {
				if _, err := hex.DecodeString(digest); err == nil {
					return "sha256:<hash>"
				}
			}
		}
		if n.replacements != nil {
			return n.replacements.Replace(value)
		}
	case json.Number:
		switch key {
		case "server_pid", "runner_pid", "reviewer_pid":
			if pid, err := value.Int64(); err == nil && pid > 0 {
				return json.Number("12345")
			}
		}
	}
	return value
}

func decodeWireJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode wire json: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("expected end of wire json, got '%v'", err)
	}
	return value
}

func marshalWireJSON(t *testing.T, value any) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		t.Fatalf("encode wire json: %v", err)
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n"))
}

func checkWireGolden(t *testing.T, name string, actual any) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		path := filepath.Join("testdata", "wire", name+".json")
		if *updateWireGoldens {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("create golden directory: %v", err)
			}
			if err := os.WriteFile(path, append(marshalWireJSON(t, actual), '\n'), 0o644); err != nil {
				t.Fatalf("write golden: %v", err)
			}
			return
		}

		expected := decodeWireJSON(t, []byte(readFileContent(t, path)))
		if !reflect.DeepEqual(expected, actual) {
			t.Errorf("wire differs from '%s'\nwant:\n%s\ngot:\n%s",
				path, marshalWireJSON(t, expected), marshalWireJSON(t, actual))
		}
	})
}

func TestWireFileNumbersKeepTheirSpelling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "numbers.json")
	if err := os.WriteFile(path, []byte(`{"large":9007199254740993,"decimal":1.50,"round_number":1,"reviewer_pid":0}`), 0o600); err != nil {
		t.Fatalf("write numbers: %v", err)
	}
	actual := (wireNormalizer{}).file(t, path)
	expected := map[string]any{
		"large":        json.Number("9007199254740993"),
		"decimal":      json.Number("1.50"),
		"round_number": json.Number("1"),
		"reviewer_pid": json.Number("0"),
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("wire numbers = %#v, want %#v", actual, expected)
	}
}

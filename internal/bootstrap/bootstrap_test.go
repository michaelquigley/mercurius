package bootstrap

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTemplateNonEmpty(t *testing.T) {
	if len(Template) == 0 {
		t.Fatal("embedded template is empty")
	}
	// the template should at least carry a reviewer block so the result is
	// runnable without further edits beyond filling in calibration text.
	if !bytes.Contains(Template, []byte("reviewer:")) {
		t.Fatalf("template missing reviewer block:\n%s", string(Template))
	}
}

func TestWriteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, false)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if want := filepath.Join(dir, ConfigFileName); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !bytes.Equal(got, Template) {
		t.Fatalf("written content does not match embedded template")
	}
}

func TestWriteRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	gotPath, err := Write(dir, false)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("expected ErrExists, got err=%v path=%q", err, gotPath)
	}
	if gotPath != path {
		t.Fatalf("returned path = %q, want %q (so caller can report it)", gotPath, path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	if string(got) != "existing" {
		t.Fatalf("existing file was overwritten: %q", got)
	}
}

func TestWriteForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := Write(dir, true); err != nil {
		t.Fatalf("write force: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after force: %v", err)
	}
	if !bytes.Equal(got, Template) {
		t.Fatalf("force-write did not replace content")
	}
}

func TestWriteRejectsMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := Write(dir, false); err == nil {
		t.Fatal("expected error when bootstrap dir does not exist")
	}
}

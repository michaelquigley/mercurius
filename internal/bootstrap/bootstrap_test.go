package bootstrap

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/michaelquigley/mercurius/internal/config"
)

// the project template must resolve as written: no reviewer block, which would
// be refused, and nothing else stale. A project bootstrapped from it runs on
// the installation's reviewer.
func TestProjectTemplateHoldsNoReviewer(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, false)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	resolved, err := config.Resolve(path, filepath.Join(dir, "no-user-config.yaml"))
	if err != nil {
		t.Fatalf("the project template must resolve: %v", err)
	}
	if resolved.Reviewer != nil {
		t.Fatalf("the project template must not name a reviewer: %+v", resolved.Reviewer)
	}
}

// the user template must supply a reviewer for every project.
func TestUserTemplateSuppliesReviewer(t *testing.T) {
	dir := t.TempDir()
	userPath, err := WriteUser(filepath.Join(dir, "config", "mercurius", "config.yaml"), false)
	if err != nil {
		t.Fatalf("write user: %v", err)
	}
	projectPath, err := Write(dir, false)
	if err != nil {
		t.Fatalf("write project: %v", err)
	}
	resolved, err := config.Resolve(projectPath, userPath)
	if err != nil {
		t.Fatalf("the templates must resolve together: %v", err)
	}
	if resolved.Reviewer == nil || resolved.ReviewerSource != config.SourceUserConfig {
		t.Fatalf("reviewer = %+v source = %q, want one from the user config", resolved.Reviewer, resolved.ReviewerSource)
	}
}

func TestWriteUserCreatesDirectoryAndRefusesOverwrite(t *testing.T) {
	target := filepath.Join(t.TempDir(), "xdg", "mercurius", "config.yaml")
	path, err := WriteUser(target, false)
	if err != nil {
		t.Fatalf("write user: %v", err)
	}
	if path != target {
		t.Fatalf("path = %q, want %q", path, target)
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, UserTemplate) {
		t.Fatalf("written user config does not match the template: err=%v", err)
	}
	if err := os.WriteFile(target, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteUser(target, false); !errors.Is(err, ErrExists) {
		t.Fatalf("expected ErrExists without force, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "mine" {
		t.Fatal("a refused overwrite changed the file")
	}
	if _, err := WriteUser(target, true); err != nil {
		t.Fatalf("force: %v", err)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, UserTemplate) {
		t.Fatal("force did not overwrite")
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

// Package bootstrap embeds the starter configuration templates and writes them:
// the project's mercurius.yaml, which holds no reviewer, and the user-level
// config, which holds the reviewer for the installation. the mercurius CLI
// exposes this through `mercurius bootstrap` and `mercurius bootstrap --user`.
package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "embed"
)

// Template is the embedded starter mercurius.yaml content.
//
//go:embed mercurius.yaml
var Template []byte

// UserTemplate is the embedded starter user-level config content.
//
//go:embed user.yaml
var UserTemplate []byte

// ConfigFileName is the canonical filename Mercurius looks for in a project.
const ConfigFileName = "mercurius.yaml"

// ErrExists is returned by Write when the target file already exists and
// force=false. the caller should surface this so the user can decide whether
// to inspect the existing file or re-run with --force.
var ErrExists = errors.New("config file already exists")

// Write writes the embedded template as `mercurius.yaml` inside dir. it
// refuses to clobber an existing file unless force is true. the returned path
// is the absolute path of the file actually written.
func Write(dir string, force bool) (string, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve bootstrap dir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("bootstrap dir '%s' is not available: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("bootstrap dir '%s' is not a directory", abs)
	}

	return writeFile(filepath.Join(abs, ConfigFileName), Template, force)
}

// WriteUser writes the embedded user-level template to path, creating its
// directory if needed. it refuses to clobber an existing file unless force is
// true. the returned path is the absolute path of the file written.
func WriteUser(path string, force bool) (string, error) {
	if path == "" {
		return "", errors.New("no user-level config path could be determined on this system")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve user config path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", fmt.Errorf("create '%s': %w", filepath.Dir(abs), err)
	}
	return writeFile(abs, UserTemplate, force)
}

func writeFile(target string, content []byte, force bool) (string, error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(target, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return target, ErrExists
		}
		return "", fmt.Errorf("create '%s': %w", target, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write '%s': %w", target, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close '%s': %w", target, err)
	}
	return target, nil
}

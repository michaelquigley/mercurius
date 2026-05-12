// Package bootstrap embeds the starter mercurius.yaml template and writes it
// to a destination directory. The mercurius CLI exposes this through the
// `mercurius bootstrap` subcommand so a new project can be initialized without
// hunting for an example file.
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

// ConfigFileName is the canonical filename Mercurius looks for in a project.
const ConfigFileName = "mercurius.yaml"

// ErrExists is returned by Write when the target file already exists and
// force=false. The caller should surface this so the user can decide whether
// to inspect the existing file or re-run with --force.
var ErrExists = errors.New("config file already exists")

// Write writes the embedded template as `mercurius.yaml` inside dir. It
// refuses to clobber an existing file unless force is true. The returned path
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

	target := filepath.Join(abs, ConfigFileName)
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
	if _, err := file.Write(Template); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write '%s': %w", target, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close '%s': %w", target, err)
	}
	return target, nil
}

//go:build !linux

package reviewer

import "os/exec"

// prepareLifetime does nothing off Linux: the parent-death signal and the
// process-group kill are Linux arrangements.
func prepareLifetime(cmd *exec.Cmd) {}

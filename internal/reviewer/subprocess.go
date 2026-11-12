package reviewer

import (
	"bytes"
	"errors"
	"os/exec"
	"time"
)

// WaitDelay bounds how long a subprocess reviewer's Wait may block once the
// child has exited or its context is done. Without it, Wait returns only when
// the child's output pipes reach end of file, which a process the reviewer
// started can hold off indefinitely, so a round timeout would not be a bound. A
// package variable so tests can shorten it.
var WaitDelay = 10 * time.Second

// PrepareCommand applies the subprocess settings every subprocess reviewer
// shares. The codex, claude, and pi reviewers call it on their exec.Cmd before
// starting it. On Linux it also ties the reviewer's life to the thread that
// starts it and makes the reviewer lead its own process group, so a context
// cancel kills the group; see prepareLifetime.
func PrepareCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = WaitDelay
	prepareLifetime(cmd)
}

// RunCommand prepares cmd, starts it, reports its pid through started when set,
// and waits for it. The caller must run it on the goroutine whose OS thread is
// locked for the reviewer's whole life: the parent-death signal follows the
// thread that forked the child, not the process.
func RunCommand(cmd *exec.Cmd, started func(pid int)) error {
	PrepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	if started != nil {
		started(cmd.Process.Pid)
	}
	return cmd.Wait()
}

// OutputError is a reviewer failure that carries the output the reviewer
// captured. Reviewers return it from every failure where they hold output,
// instead of folding that output into the error message, so the runner can keep
// the output in _raw.txt and the stored error stays small.
type OutputError struct {
	Err    error
	Output []byte
}

func (e *OutputError) Error() string {
	if e.Err == nil {
		return "reviewer failed"
	}
	return e.Err.Error()
}

func (e *OutputError) Unwrap() error {
	return e.Err
}

// NewOutputError wraps err with the labelled, non-empty output sections given
// as name/bytes pairs. With no non-empty section it returns err unchanged.
func NewOutputError(err error, sections ...OutputSection) error {
	output := JoinOutput(sections...)
	if len(output) == 0 {
		return err
	}
	return &OutputError{Err: err, Output: output}
}

// OutputSection is one labelled piece of captured reviewer output.
type OutputSection struct {
	Name  string
	Bytes []byte
}

// JoinOutput joins the non-empty sections under '==> name <==' headers.
func JoinOutput(sections ...OutputSection) []byte {
	var b bytes.Buffer
	for _, section := range sections {
		if len(section.Bytes) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("==> ")
		b.WriteString(section.Name)
		b.WriteString(" <==\n")
		b.Write(section.Bytes)
		if !bytes.HasSuffix(section.Bytes, []byte("\n")) {
			b.WriteString("\n")
		}
	}
	return b.Bytes()
}

// CapturedOutput returns the output carried by err, if any.
func CapturedOutput(err error) []byte {
	var outputErr *OutputError
	if errors.As(err, &outputErr) {
		return outputErr.Output
	}
	return nil
}

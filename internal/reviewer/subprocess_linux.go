package reviewer

import (
	"os/exec"
	"syscall"
)

// prepareLifetime ties a reviewer subprocess to its runner. Pdeathsig makes the
// kernel kill the reviewer when the thread that started it dies, which is the
// runner's locked thread, so a killed runner takes its reviewer with it. Setpgid
// makes the reviewer lead its own process group, and Cancel kills that group, so
// the round timeout takes the reviewer's own children too. The group is named by
// the pid of the process this command just started, never by a pid read from
// disk.
func prepareLifetime(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

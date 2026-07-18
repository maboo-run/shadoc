//go:build !windows

package command

import (
	"os/exec"
	"syscall"
)

func prepareCommand(cmd *exec.Cmd)         { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func interruptProcess(cmd *exec.Cmd) error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
func killProcess(cmd *exec.Cmd) error      { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

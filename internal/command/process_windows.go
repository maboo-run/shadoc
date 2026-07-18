//go:build windows

package command

import (
	"os"
	"os/exec"
)

func prepareCommand(*exec.Cmd)             {}
func interruptProcess(cmd *exec.Cmd) error { return cmd.Process.Signal(os.Interrupt) }
func killProcess(cmd *exec.Cmd) error      { return cmd.Process.Kill() }

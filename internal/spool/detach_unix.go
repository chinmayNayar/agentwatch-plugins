//go:build !windows

package spool

import (
	"os"
	"os/exec"
	"syscall"
)

// StartFlusher starts `<exe> flush` in its own session so it outlives the
// hook and the client that started the hook.
func StartFlusher(exe string) error {
	cmd := exec.Command(exe, "flush")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

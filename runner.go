package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

type Runner interface {
	Run(context.Context, []string, []byte, string, ...string) ([]byte, error)
}
type ExecRunner struct{}
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 8<<20 {
		left := (8 << 20) - b.Len()
		if len(p) > left {
			p = p[:left]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
func (ExecRunner) Run(ctx context.Context, env []string, in []byte, cmd string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, cmd, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.Env = append(os.Environ(), env...)
	c.Stdin = bytes.NewReader(in)
	var out cappedBuffer
	c.Stdout = &out
	c.Stderr = io.Discard
	if e := c.Run(); e != nil {
		return nil, fmt.Errorf("%s failed (%v); subprocess output suppressed to protect secrets", cmd, e)
	}
	if out.Len() >= 8<<20 {
		return nil, fmt.Errorf("%s output exceeded limit", cmd)
	}
	return out.Bytes(), nil
}

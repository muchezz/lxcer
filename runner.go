package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

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
func runCommand(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	return runOutput(ctx, nil, nil, cmd, args...)
}

func runOutput(ctx context.Context, env []string, in []byte, cmd string, args ...string) ([]byte, error) {
	var out cappedBuffer
	if err := runStream(ctx, env, bytes.NewReader(in), &out, cmd, args...); err != nil {
		return nil, err
	}
	if out.Len() >= 8<<20 {
		return nil, fmt.Errorf("%s output exceeded limit", cmd)
	}
	return out.Bytes(), nil
}

func runStream(ctx context.Context, env []string, in io.Reader, out io.Writer, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	if cmd == "restic" {
		// Allow Restic to roll back an interrupted operation and release its lock.
		c.Cancel = func() error { return c.Process.Signal(os.Interrupt) }
		c.WaitDelay = 5 * time.Second
	}
	c.Env = append(os.Environ(), env...)
	c.Stdin = in
	c.Stdout = out
	c.Stderr = io.Discard
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		return fmt.Errorf("%s failed: %w", cmd, err)
	}
	return nil
}

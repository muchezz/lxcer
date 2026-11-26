package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// The monitor bounds growth by cancellation; hard byte limits require filesystem quotas.
type stagingGuard struct {
	C    Config
	Dir  string
	peak atomic.Int64
}

func directoryBytes(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, e := d.Info()
			if e != nil {
				if errors.Is(e, os.ErrNotExist) {
					return nil
				}
				return e
			}
			n += info.Size()
		}
		return nil
	})
	return n, err
}
func (g *stagingGuard) check() error {
	n, err := directoryBytes(g.Dir)
	if err != nil {
		return err
	}
	for old := g.peak.Load(); n > old; old = g.peak.Load() {
		if g.peak.CompareAndSwap(old, n) {
			break
		}
	}
	if g.C.MaxStagingBytes > 0 && uint64(n) > g.C.MaxStagingBytes {
		return errors.New("restore or job exceeds staging byte limit")
	}
	if err = freeSpace(g.Dir, g.C.MinFreeBytes); err != nil {
		return err
	}
	if err = freeSpace(g.C.StateDir, g.C.MinFreeBytes); err != nil {
		return err
	}
	if g.C.MaxStagingBytes > 0 {
		var total int64
		entries, er := os.ReadDir(g.C.StateDir)
		if er != nil {
			return er
		}
		for _, d := range entries {
			if d.IsDir() && (strings.HasPrefix(d.Name(), "lxcer-") || strings.HasPrefix(d.Name(), "restore-") || strings.HasPrefix(d.Name(), "adopt-")) {
				size, er := directoryBytes(filepath.Join(g.C.StateDir, d.Name()))
				if er != nil {
					if errors.Is(er, os.ErrNotExist) {
						continue
					}
					return er
				}
				total += size
			}
		}
		if total < 0 || uint64(total) > g.C.MaxStagingBytes {
			return fmt.Errorf("staging budget exceeded (%d bytes, limit %d); recovery data retained", total, g.C.MaxStagingBytes)
		}
	}
	return nil
}
func (e Engine) guard(ctx context.Context, dir string) (context.Context, *stagingGuard, func()) {
	child, cancel := context.WithCancelCause(ctx)
	g := &stagingGuard{C: e.C, Dir: dir}
	done := make(chan struct{})
	quit := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-child.Done():
				return
			case <-ticker.C:
				if err := g.check(); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return child, g, func() { close(quit); <-done; cancel(nil) }
}

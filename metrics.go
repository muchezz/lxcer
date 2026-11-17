package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Textfile metrics are recomputed from durable manifests, including interrupted jobs.
func (e Engine) writeMetrics() error {
	if e.C.MetricsFile == "" {
		return nil
	}
	lock, err := acquireLockMode(e.C.StateDir, "metrics", true)
	if err != nil {
		return err
	}
	defer releaseLock(lock)
	jobs, err := filepath.Glob(filepath.Join(e.C.StateDir, "lxcer-*", "manifest.json"))
	if err != nil {
		return err
	}
	type series struct {
		latest       Manifest
		success      time.Time
		failures     int
		verification int
		running      int
	}
	groups := map[string]*series{}
	for _, path := range jobs {
		m, er := loadManifest(path)
		if er != nil {
			return er
		}
		key := m.Host + "/" + m.Instance.Name
		g := groups[key]
		if g == nil {
			g = &series{}
			groups[key] = g
		}
		if m.Started.After(g.latest.Started) {
			g.latest = m
		}
		if m.State == Verified && m.Completed.After(g.success) {
			g.success = m.Completed
		}
		if m.State == Failed || m.State == PartiallyCompleted {
			g.failures++
		}
		for _, d := range m.Destinations {
			if !d.Verified {
				g.verification++
			}
		}
		if m.Completed.IsZero() {
			g.running++
		}
	}
	var b strings.Builder
	types := map[string]string{"lxcer_backup_success": "gauge", "lxcer_backup_last_success_timestamp_seconds": "gauge", "lxcer_backup_duration_seconds": "gauge", "lxcer_backup_size_bytes": "gauge", "lxcer_backup_failures_total": "counter", "lxcer_backup_jobs_running": "gauge", "lxcer_backup_verification_failures_total": "counter", "lxcer_restore_test_success": "gauge", "lxcer_restore_test_duration_seconds": "gauge"}
	keys := make([]string, 0, len(types))
	for k := range types {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "# HELP %s lxcer durable catalog metric\n# TYPE %s %s\n", k, k, types[k])
	}
	label := func(m Manifest) string {
		return `{host=` + strconv.Quote(m.Host) + `,instance=` + strconv.Quote(m.Instance.Name) + `}`
	}
	for _, g := range groups {
		m := g.latest
		l := label(m)
		success := 0
		if m.State == Verified {
			success = 1
		}
		ts := int64(0)
		if !g.success.IsZero() {
			ts = g.success.Unix()
		}
		duration := float64(0)
		if !m.Completed.IsZero() {
			duration = m.Completed.Sub(m.Started).Seconds()
		}
		fmt.Fprintf(&b, "lxcer_backup_success%s %d\nlxcer_backup_last_success_timestamp_seconds%s %d\nlxcer_backup_duration_seconds%s %g\nlxcer_backup_size_bytes%s %d\nlxcer_backup_failures_total%s %d\nlxcer_backup_jobs_running%s %d\nlxcer_backup_verification_failures_total%s %d\n", l, success, l, ts, l, duration, l, m.Size, l, g.failures, l, g.running, l, g.verification)
		if m.RestoreTest != nil {
			v := 0
			if m.RestoreTest.Success {
				v = 1
			}
			fmt.Fprintf(&b, "lxcer_restore_test_success%s %d\nlxcer_restore_test_duration_seconds%s %g\n", l, v, l, m.RestoreTest.Duration)
		}
	}
	if _, err = os.Stat(filepath.Dir(e.C.MetricsFile)); err != nil {
		return err
	}
	return atomicWriteMode(e.C.MetricsFile, []byte(b.String()), 0644)
}

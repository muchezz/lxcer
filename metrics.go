package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type backupRun struct {
	Started   time.Time `json:"started"`
	Completed time.Time `json:"completed"`
	Success   bool      `json:"success"`
}

// Textfile metrics are recomputed from durable manifests and the latest completed invocation.
func (e Engine) writeMetrics(run *backupRun) error {
	if e.C.MetricsFile == "" {
		return nil
	}
	if err := privateDir(e.C.StateDir); err != nil {
		return err
	}
	lock, err := acquireLockMode(e.C.StateDir, "metrics", true)
	if err != nil {
		return err
	}
	defer releaseLock(lock)
	runPath := filepath.Join(e.C.StateDir, "backup-run.json")
	var lastRun backupRun
	if b, err := os.ReadFile(runPath); err == nil {
		if err := json.Unmarshal(b, &lastRun); err != nil {
			return fmt.Errorf("read backup run status: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if run != nil && run.Started.After(lastRun.Started) {
		b, err := json.Marshal(run)
		if err != nil {
			return err
		}
		if err := atomicWrite(runPath, b); err != nil {
			return err
		}
		lastRun = *run
	}
	jobs, err := manifestPaths(e.C.StateDir)
	if err != nil {
		return err
	}
	type series struct {
		latest       Manifest
		restore      *RestoreTest
		success      time.Time
		failures     int
		verification int
		unfinished   int
		lastStarted  time.Time
		remote       string
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
		if m.RestoreTest != nil && (g.restore == nil || m.RestoreTest.Time.After(g.restore.Time)) {
			g.restore = m.RestoreTest
		}
		if m.Legacy {
			continue
		}
		if m.Completed.IsZero() {
			g.unfinished++
			if m.Started.After(g.lastStarted) {
				g.lastStarted = m.Started
				g.remote = sourceRemote(m)
			}
			continue
		}
		if m.Started.After(g.latest.Started) {
			g.latest = m
		}
		if m.State == Verified && m.Error == "" && !m.NeedsResume && m.verificationComplete() {
			if m.Completed.After(g.success) {
				g.success = m.Completed
			}
		} else {
			g.failures++
		}
		for _, d := range m.Destinations {
			if !d.Verified || (d.Error != "" && !snapshotID(d.ReceiptID)) {
				g.verification++
			}
		}
	}
	type sample struct {
		labels string
		value  string
	}
	samples := map[string][]sample{}
	add := func(name, labels string, value any) {
		samples[name] = append(samples[name], sample{labels: labels, value: fmt.Sprint(value)})
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		g := groups[key]
		host, instance, _ := strings.Cut(key, "/")
		labels := "host=" + strconv.Quote(host) + ",instance=" + strconv.Quote(instance)
		if g.restore != nil {
			add("restore_test_success", labels, metricBool(g.restore.Success))
			add("restore_test_duration_seconds", labels, g.restore.Duration)
		}
		m := g.latest
		if !m.Completed.IsZero() || g.unfinished > 0 {
			running := 0
			if g.unfinished > 0 && g.lastStarted.After(m.Started) {
				lock, err := acquireLock(e.C.StateDir, g.remote+"/"+instance)
				if errors.Is(err, errJobLocked) {
					running = 1
				} else if err != nil {
					return err
				}
				releaseLock(lock)
			}
			add("backup_jobs_running", labels, running)
			add("backup_jobs_interrupted", labels, g.unfinished-running)
		}
		if m.Completed.IsZero() {
			continue
		}

		add("backup_capture_duration_seconds", labels, m.CaptureSeconds)
		add("backup_staging_peak_bytes", labels, m.StagingPeakBytes)
		chain := 0
		if m.Stream != nil {
			chain = len(m.Stream.Parents) + 1
		}
		add("backup_chain_length", labels, chain)
		for _, d := range m.Destinations {
			add("backup_upload_bytes", labels+",destination="+strconv.Quote(d.Name), d.UploadBytes)
		}
		verified := !m.NeedsResume && m.verificationComplete()
		add("backup_required_destinations_verified", labels, metricBool(verified))
		add("backup_success", labels, metricBool(m.State == Verified && m.Error == "" && verified))
		lastSuccess := int64(0)
		if !g.success.IsZero() {
			lastSuccess = g.success.Unix()
		}
		add("backup_last_success_timestamp_seconds", labels, lastSuccess)
		add("backup_duration_seconds", labels, m.Completed.Sub(m.Started).Seconds())
		size := m.Size
		for _, part := range m.ImageParts {
			size += part.Size
		}
		for _, volume := range m.Volumes {
			size += volume.Size
		}
		if size > 0 {
			add("backup_size_bytes", labels, size)
		}
		add("backup_failures_total", labels, g.failures)
		add("backup_verification_failures_total", labels, g.verification)
	}

	if !lastRun.Completed.IsZero() {
		add("backup_run_success", "", metricBool(lastRun.Success))
		add("backup_run_last_completion_timestamp_seconds", "", lastRun.Completed.Unix())
	}
	names := make([]string, 0, len(samples))
	for name := range samples {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		kind := "gauge"
		if strings.HasSuffix(name, "_total") {
			kind = "counter"
		}
		fmt.Fprintf(&b, "# HELP lxcer_%s lxcer %s.\n# TYPE lxcer_%s %s\n", name, strings.ReplaceAll(name, "_", " "), name, kind)
		for _, s := range samples[name] {
			if s.labels == "" {
				fmt.Fprintf(&b, "lxcer_%s %s\n", name, s.value)
			} else {
				fmt.Fprintf(&b, "lxcer_%s{%s} %s\n", name, s.labels, s.value)
			}
		}
	}
	return atomicWriteMode(e.C.MetricsFile, []byte(b.String()), 0644)
}

func metricBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

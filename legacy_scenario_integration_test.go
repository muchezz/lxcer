//go:build lxdintegration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type verificationFailureRunner struct{ Runner }

func (r verificationFailureRunner) Run(ctx context.Context, env []string, b []byte, cmd string, a ...string) ([]byte, error) {
	if cmd == "restic" && len(a) > 0 && a[0] == "ls" {
		return nil, fmt.Errorf("injected missing repository tree")
	}
	if cmd == "lxc" && len(a) > 0 && a[0] == "export" && a[1] != "--help" {
		return nil, fmt.Errorf("LXD 3.0.3 scenario must not use lxc export")
	}
	return r.Runner.Run(ctx, env, b, cmd, a...)
}
func requireRootProfile(ctx context.Context, l LXD, name string) error {
	b, err := l.run(ctx, "query", l.ref("/1.0/profiles/"+name))
	if err != nil {
		return err
	}
	b, err = unwrapAPI(b)
	if err != nil {
		return err
	}
	var p struct {
		Config  map[string]string            `json:"config"`
		Devices map[string]map[string]string `json:"devices"`
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return err
	}
	for key := range p.Config {
		if strings.HasPrefix(key, "raw.") || strings.HasPrefix(key, "security.") {
			return fmt.Errorf("test profile contains unsafe configuration")
		}
	}
	roots := 0
	for _, d := range p.Devices {
		if d["type"] != "disk" || d["path"] != "/" {
			return fmt.Errorf("test profile must be root-only")
		}
		roots++
	}
	if roots != 1 {
		return fmt.Errorf("test profile must have exactly one root disk")
	}
	return nil
}
func TestLegacy303BtrfsSFTP(t *testing.T) {
	source := os.Getenv("LXCER_TEST_REMOTE")
	target := os.Getenv("LXCER_TEST_RESTORE_REMOTE")
	profile := os.Getenv("LXCER_TEST_PROFILE")
	targetProfile := os.Getenv("LXCER_TEST_RESTORE_PROFILE")
	image := os.Getenv("LXCER_TEST_IMAGE")
	repo := os.Getenv("LXCER_TEST_SFTP_REPOSITORY")
	if os.Getenv("LXCER_TEST_DISPOSABLE") != "YES" {
		t.Skip("NOT VERIFIED: disposable integration was not enabled")
	}
	if !strings.HasPrefix(source, "lxcer-test-") || !strings.HasPrefix(target, "lxcer-test-") || source == target || !validName(source) || !validName(target) || !validName(profile) || !validName(targetProfile) || image == "" || strings.HasPrefix(image, "-") || !strings.HasPrefix(repo, "sftp:") || !strings.Contains(repo, "/lxcer-test-") {
		t.Fatal("NOT VERIFIED: requires two disposable lxcer-test-* hosts, root-only profiles and a dedicated /lxcer-test-* SFTP repository")
	}
	if _, ok := os.LookupEnv("LXCER_TEST_RESTIC_PASSWORD"); !ok {
		t.Fatal("test repository password environment source required")
	}
	size := 256
	if value := os.Getenv("LXCER_TEST_PAYLOAD_MIB"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 16 || n > 4096 {
			t.Fatal("payload must be 16..4096 MiB")
		}
		size = n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	runner := ExecRunner{}
	l := LXD{R: runner, Host: source}
	if err := l.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.SplitN(l.Caps.Server, "-", 2)[0] != "3.0.3" {
		t.Fatalf("NOT VERIFIED: expected LXD 3.0.3, got %s", l.Caps.Server)
	}
	if err := requireRootProfile(ctx, l, profile); err != nil {
		t.Fatal(err)
	}
	restoreHost := LXD{R: runner, Host: target}
	if err := requireRootProfile(ctx, restoreHost, targetProfile); err != nil {
		t.Fatal(err)
	}
	e := testEngine(t, fake())
	e.R = runner
	e.C.Timeout = time.Hour
	e.C.MaxStagingBytes = 16 << 30
	e.C.MinFreeBytes = 2 << 30
	e.C.Policy = Policy{Strategy: "snapshot"}
	e.C.Compression = "zstd"
	e.C.Destinations = []Destination{{Name: "sftp-required", Path: repo, PasswordEnv: "LXCER_TEST_RESTIC_PASSWORD", ReadData: false}}
	if _, err := e.repo(e.C.Destinations[0]).run(ctx, "init"); err != nil {
		t.Fatal("dedicated test repository must be new and reachable:", err)
	}
	var names []string
	payloadHash := ""
	for _, running := range []bool{true, false} {
		name := newID()
		names = append(names, name)
		if _, err := l.run(ctx, "init", image, l.ref(name), "-p", profile, "-c", "boot.autostart=false", "-c", "user.lxcer.integration="+name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cc, err := l.List(cleanup)
			if err != nil {
				t.Error(err)
				return
			}
			for _, c := range cc {
				if c.Name == name && c.Config["user.lxcer.integration"] == name {
					if _, err = l.run(cleanup, "delete", l.ref(name), "--force"); err != nil {
						t.Error(err)
					}
				}
			}
		})
		cc, err := l.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cc {
			if c.Name == name {
				driver, err := l.Storage(ctx, c)
				if err != nil || driver != "btrfs" {
					t.Fatalf("NOT VERIFIED: source must be Btrfs, driver=%s err=%v", driver, err)
				}
			}
		}
		if running {
			if _, err = l.run(ctx, "start", l.ref(name)); err != nil {
				t.Fatal(err)
			}
			if _, err = l.run(ctx, "exec", l.ref(name), "--", "dd", "if=/dev/urandom", "of=/root/lxcer-test-payload", "bs=1M", "count="+strconv.Itoa(size)); err != nil {
				t.Fatal(err)
			}
			hashBytes, hashErr := l.run(ctx, "exec", l.ref(name), "--", "sha256sum", "/root/lxcer-test-payload")
			if hashErr != nil {
				t.Fatal(hashErr)
			}
			hashFields := strings.Fields(string(hashBytes))
			if len(hashFields) == 0 {
				t.Fatal("checksum command returned no checksum")
			}
			payloadHash = hashFields[0]
			if len(payloadHash) != 64 || !snapshotID(payloadHash) {
				t.Fatal("invalid payload checksum")
			}
			if _, err = l.run(ctx, "exec", l.ref(name), "--", "sh", "-c", "(while :; do date >> /root/lxcer-test-heartbeat; sleep 1; done) </dev/null >/dev/null 2>&1 &"); err != nil {
				t.Fatal(err)
			}
		}
		if err = e.Backup(ctx, []Host{{Name: source}}, name); err != nil {
			t.Fatal(err)
		}
		var m Manifest
		for _, job := range manifests(t, e) {
			if job.Instance.Name == name {
				m = job
			}
		}
		if m.State != Verified || m.Cleanup != "completed" || len(m.Destinations) != 1 || !m.Destinations[0].Verified || m.Destinations[0].ReceiptID == "" || m.Size <= 0 {
			t.Fatal("incomplete backup was reported as successful")
		}
		health := []string(nil)
		if running {
			health = []string{"sh", "-c", `printf "%s  /root/lxcer-test-payload\n" "$1" | sha256sum -c - && test -s /root/lxcer-test-heartbeat`, "lxcer-check", payloadHash}
		}
		if err = e.restore(ctx, &m, RestoreOptions{Name: newID(), Host: target, Destination: "sftp-required", Test: true, Isolated: true, Profile: targetProfile, Pool: os.Getenv("LXCER_TEST_POOL"), Health: health}); err != nil {
			t.Fatal(err)
		}
		if m.RestoreTest == nil || !m.RestoreTest.Success {
			t.Fatal("restore verification failed")
		}
		t.Logf("PASS legacy source=%s driver=btrfs running=%v artifact_bytes=%d packed_upload_bytes=%d capture_seconds=%g duration_seconds=%g staging_peak_bytes=%d restore_seconds=%g", l.Caps.Server, running, m.Size, m.Destinations[0].UploadBytes, m.CaptureSeconds, m.Completed.Sub(m.Started).Seconds(), m.StagingPeakBytes, m.RestoreTest.Duration)
	}
	if _, err := e.repo(e.C.Destinations[0]).run(ctx, "check", "--read-data"); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual CLI error path with a deliberately unverifiable repository tree.
	config := filepath.Join(t.TempDir(), "scenario.yml")
	b, err := yaml.Marshal(e.C)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(config, b, 0600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if code := runWithRunner([]string{"-a", "backup", "-config", config, "-remote-host", source, "-container", names[1]}, &log, &log, verificationFailureRunner{runner}); code == 0 {
		t.Fatal("FAIL: incomplete legacy backup exited zero")
	}
	retained := false
	for _, m := range manifests(t, e) {
		if m.State == Failed {
			retained = true
			if m.Cleanup == "completed" {
				t.Fatal("incomplete backup cleaned its recovery copy")
			}
			if _, err = os.Stat(m.Artifact); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !retained {
		t.Fatal("missing failure evidence")
	}
}

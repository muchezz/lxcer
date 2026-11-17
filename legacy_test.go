package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginalConfigDefaultsPreserved(t *testing.T) {
	file := filepath.Join(t.TempDir(), "legacy.yml")
	if err := os.WriteFile(file, []byte("hosts: [original]\nlocal_workers: 2\nbackup_restic_repos:\n - path: /repo\n   password: old\nrestore_restic_repo:\n path: /repo\n password: old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := readConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Legacy || c.Policy.Strategy != "snapshot" || c.Compression != "zstd" || c.Workers != 2 || c.RestoreResticRepo.Path != "/repo" {
		t.Fatalf("original defaults changed: %+v", c)
	}
}
func TestOriginalRestoreFlagsAndArchives(t *testing.T) {
	if testing.Short() {
		t.Skip("real legacy CLI/Restic integration")
	}
	for _, cmd := range []string{"restic", "zstd"} {
		if _, err := exec.LookPath(cmd); err != nil {
			t.Skip(cmd + " unavailable")
		}
	}
	ctx := context.Background()
	f := fake()
	f.realRestic = true
	e := testEngine(t, f)
	repo := filepath.Join(t.TempDir(), "repo")
	e.C.Destinations[0] = Destination{Name: "repo-1", Path: repo, Password: "isolated-test-password"}
	r := e.repo(e.C.Destinations[0])
	if _, err := r.run(ctx, "init"); err != nil {
		t.Fatal(err)
	}
	oldTar := filepath.Join(t.TempDir(), "old.tar")
	if err := os.WriteFile(oldTar, []byte("original image archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (ExecRunner{}).Run(ctx, nil, nil, "zstd", oldTar, "-o", oldTar+".zst"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.backup(ctx, []string{oldTar + ".zst"}, nil); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.yml")
	text := fmt.Sprintf("hosts: []\nlocal_workers: 2\nmin_free_bytes: 0\nstate_dir: %s\nbackup_restic_repos:\n - path: %s\n   password: isolated-test-password\nrestore_restic_repo:\n path: %s\n password: isolated-test-password\n", e.C.StateDir, repo, repo)
	if err := os.WriteFile(config, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// The exact original invocation needs neither -destination nor -manifest/adoption.
	args := []string{"-config", config, "-a", "restore", "-container", "old", "-as", "old-restored", "-remote-host", "lab"}
	if code := runWithRunner(args, &out, &out, f); code != 0 {
		t.Fatalf("legacy restore failed: %s", out.String())
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "lxc start lab:old-restored") {
		t.Fatal("original restore no longer starts its instance")
	}
	before := len(f.calls)
	out.Reset()
	args = []string{"-config", config, "-a", "restore", "-container", "old", "-as", "old-stopped", "-remote-host", "lab", "-stopped"}
	if code := runWithRunner(args, &out, &out, f); code != 0 {
		t.Fatal(out.String())
	}
	if strings.Contains(strings.Join(f.calls[before:], "\n"), "lxc start lab:old-stopped") {
		t.Fatal("-stopped ignored")
	}
	// Legacy backup keeps snapshot publication, zstd and the instance archive basename.
	out.Reset()
	args = []string{"-config", config, "-a", "backup", "-local", "-container", "stopped"}
	if code := runWithRunner(args, &out, &out, f); code != 0 {
		t.Fatal(out.String())
	}
	mm := manifests(t, e)
	if len(mm) != 1 || mm[0].Strategy != "snapshot" || filepath.Base(mm[0].Artifact) != "stopped.tar.zst" {
		t.Fatalf("legacy backup workflow changed: %+v", mm)
	}
	// Newly cataloged backups also keep the original repository and startup flags.
	out.Reset()
	args = []string{"-config", config, "-a", "restore", "-container", "stopped", "-as", "catalog-restored", "-remote-host", "lab"}
	if code := runWithRunner(args, &out, &out, f); code != 0 {
		t.Fatal(out.String())
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "lxc start lab:catalog-restored") {
		t.Fatal("catalog restore startup changed")
	}
	list := filepath.Join(t.TempDir(), "restore.list")
	if err := os.WriteFile(list, []byte("old:concurrent-old\nstopped:concurrent-new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	args = []string{"-config", config, "-a", "restore", "-restore-list", list, "-remote-host", "lab", "-concurrently"}
	if code := runWithRunner(args, &out, &out, f); code != 0 {
		t.Fatal(out.String())
	}
	for _, name := range []string{"concurrent-old", "concurrent-new"} {
		if !strings.Contains(strings.Join(f.calls, "\n"), "lxc start lab:"+name) {
			t.Fatal("concurrent restore list changed")
		}
	}
}

func TestLegacyRestoreMissingAndCollision(t *testing.T) {
	for _, collision := range []bool{false, true} {
		f := fake()
		e := testEngine(t, f)
		if collision {
			f.instances["lab"] = []Container{instance("restored", 102)}
		}
		if err := e.restoreLegacy(context.Background(), "missing", "restored", "lab", "primary", "", true); err == nil {
			t.Fatal("missing archive or collision succeeded")
		}
		for _, call := range f.calls {
			if strings.HasPrefix(call, "lxc init") || strings.HasPrefix(call, "lxc start") {
				t.Fatal("failure mutated target")
			}
		}
	}
}
func TestAmbiguousCatalogCannotFallBackToLegacy(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	for _, host := range []string{"one", "two"} {
		id := newID()
		dir := filepath.Join(e.C.StateDir, id)
		if err := privateDir(dir); err != nil {
			t.Fatal(err)
		}
		m := Manifest{Version: 1, ID: id, Host: host, Instance: instance("app", 102), State: Verified}
		if err := m.save(dir); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "legacy.yml")
	text := fmt.Sprintf("state_dir: %s\nrestore_restic_repo:\n path: /isolated-fake-repo\n", e.C.StateDir)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runWithRunner([]string{"-a", "restore", "-config", path, "-container", "app", "-as", "app-copy", "-remote-host", "lab"}, &out, &out, f); code == 0 {
		t.Fatal("ambiguous source accepted")
	}
	if len(f.calls) != 0 {
		t.Fatal("ambiguous catalog fell back to a legacy archive")
	}
}

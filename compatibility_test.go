package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type compatibilityRunner struct {
	*fakeRunner
	version, driver, archiveName string
	split                        bool
	operations                   []string
}

func (r *compatibilityRunner) Run(ctx context.Context, env []string, in []byte, cmd string, a ...string) ([]byte, error) {
	if cmd == "lxc" {
		call := strings.Join(a, " ")
		r.operations = append(r.operations, call)
		if a[0] == "query" {
			if strings.HasSuffix(a[1], ":/1.0") {
				ext := []string{"storage", "image_compression_algorithm"}
				if r.version != "3.0.3" {
					ext = append(ext, "container_backup", "container_backup_override_pool")
				}
				if strings.HasPrefix(r.version, "5.") || strings.HasPrefix(r.version, "6.") {
					ext = append(ext, "instances", "backup_override_name")
				}
				return json.Marshal(map[string]any{"metadata": map[string]any{"environment": map[string]string{"server_version": r.version}, "api_extensions": ext}})
			}
			if strings.Contains(a[1], "/storage-pools/") {
				return []byte(`{"driver":"` + r.driver + `"}`), nil
			}
		}
		if a[0] == "version" {
			return []byte("Client version: " + r.version), nil
		}
		if a[0] == "export" && r.version == "3.0.3" {
			return nil, errors.New("unknown command: export")
		}
		if a[0] == "image" && a[1] == "export" && r.split {
			if err := os.WriteFile(a[3], []byte("image metadata"), 0600); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(a[3]+".root", []byte("root filesystem"), 0600)
		}
		if a[0] == "import" && (len(a) == 3 || (len(a) > 3 && strings.HasPrefix(a[3], "-"))) {
			a = append(append(append([]string{}, a[:3]...), r.archiveName), a[3:]...)
		}
	}
	return r.fakeRunner.Run(ctx, env, in, cmd, a...)
}
func TestBackupRestoreCompatibilityMatrix(t *testing.T) {
	for _, version := range []string{"3.0.3", "4.0", "5.0", "6.0"} {
		for _, driver := range []string{"btrfs", "zfs", "lvm", "dir", "ceph"} {
			for _, state := range []int{102, 103} {
				t.Run(fmt.Sprintf("%s/%s/%d", version, driver, state), func(t *testing.T) {
					base := fake()
					base.instances["local"] = []Container{instance("app", state)}
					r := &compatibilityRunner{fakeRunner: base, version: version, driver: driver, archiveName: "app"}
					e := testEngine(t, base)
					e.R = r
					if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "app"); err != nil {
						t.Fatal(err)
					}
					m := manifests(t, e)[0]
					if version == "3.0.3" {
						if m.Strategy != "snapshot" {
							t.Fatal("3.0.3 must use image adapter")
						}
						for _, op := range r.operations {
							if strings.HasPrefix(op, "export ") && op != "export --help" {
								t.Fatal("legacy source attempted lxc export")
							}
						}
					}
					target := "recovered"
					if version == "4.0" {
						target = "app"
					}
					if err := e.restore(context.Background(), &m, RestoreOptions{Name: target, Host: "lab", Destination: "primary", Pool: "recovery", Networks: map[string]string{"prod": "isolated"}}); err != nil {
						t.Fatal(err)
					}
					if base.instances["lab"][0].StatusCode != 102 {
						t.Fatal("restored application started")
					}
				})
			}
		}
	}
}
func TestSplitImageExportAndRestore(t *testing.T) {
	base := fake()
	r := &compatibilityRunner{fakeRunner: base, version: "3.0.3", driver: "btrfs", split: true}
	e := testEngine(t, base)
	e.R = r
	e.C.Policy.Strategy = "snapshot"
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	m := manifests(t, e)[0]
	if len(m.ImageParts) != 1 || m.ImageParts[0].Size == 0 {
		t.Fatal("split root not captured")
	}
	if err := e.restore(context.Background(), &m, RestoreOptions{Name: "split-restored", Host: "lab", Destination: "primary"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range r.operations {
		if strings.HasPrefix(op, "image import ") && strings.Contains(op, ".root") {
			found = true
		}
	}
	if !found {
		t.Fatal("split root not passed to LXD import")
	}
}
func TestSourceIdentityAndRecoveryLock(t *testing.T) {
	base := fake()
	e := testEngine(t, base)
	e.C.HostID = "agent-one"
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	m := manifests(t, e)[0]
	if m.Host != "agent-one" || m.LXDRemote != "local" {
		t.Fatal("source identity lost")
	}
	lock, err := acquireLock(e.C.StateDir, "agent-one/stopped")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock(lock)
	before := len(base.calls)
	if err = e.recoverJobs(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(base.calls) != before {
		t.Fatal("recovery mutated an actively locked job")
	}
}
func TestNativeReconstructionThroughRestic(t *testing.T) {
	for _, cmd := range []string{"restic", "tar"} {
		if _, err := exec.LookPath(cmd); err != nil {
			t.Skip(cmd + " unavailable")
		}
	}
	if testing.Short() {
		t.Skip("real Restic native reconstruction")
	}
	base := fake()
	base.realRestic = true
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "native-check"), []byte("reconstruction sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	source := base.instances["local"][1]
	source.Config["volatile.last_state.idmap"] = fmt.Sprintf(`[{"Isuid":true,"Hostid":%d,"Nsid":0,"Maprange":1},{"Isgid":true,"Hostid":%d,"Nsid":0,"Maprange":1}]`, os.Getuid(), os.Getgid())
	base.instances["local"][1] = source
	e := testEngine(t, base)
	e.R = runnerFunc(func(ctx context.Context, env []string, in []byte, cmd string, a ...string) ([]byte, error) {
		if cmd == "tar" {
			return (ExecRunner{}).Run(ctx, env, in, cmd, a...)
		}
		return base.Run(ctx, env, in, cmd, a...)
	})
	e.C.Policy = Policy{Strategy: "native", NativeRoot: root}
	e.C.Destinations[0].Path = filepath.Join(t.TempDir(), "repository")
	e.C.Destinations[0].Password = "isolated-native-test"
	r := e.repo(e.C.Destinations[0])
	if _, err := r.run(context.Background(), "init"); err != nil {
		t.Fatal(err)
	}
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	m := manifests(t, e)[0]
	if err := e.restore(context.Background(), &m, RestoreOptions{Name: "native-recovered", Host: "lab", Destination: "primary", Test: true, Isolated: true, Profile: "isolated", Health: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if m.RestoreTest == nil || !m.RestoreTest.Success || m.RestoreTest.Duration <= 0 {
		t.Fatal("native restore test result missing")
	}
	if time.Since(m.RestoreTest.Time) > time.Minute {
		t.Fatal("restore timestamp stale")
	}
}

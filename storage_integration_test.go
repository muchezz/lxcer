//go:build storageintegration

package main

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This harness verifies real kernel streams and Restic, with a modeled LXD import.
// Real LXD qualification is a separate lxdintegration run.
type kernelRunner struct {
	*fakeRunner
	driver   string
	expected []byte
	checked  bool
}

func (r *kernelRunner) Run(ctx context.Context, env []string, in []byte, cmd string, a ...string) ([]byte, error) {
	if cmd == "lxc" {
		if a[0] == "query" && strings.Contains(a[1], "/storage-pools/") {
			return []byte(`{"driver":"` + r.driver + `"}`), nil
		}
		if a[0] == "image" && a[1] == "import" {
			f, err := os.Open(a[2])
			if err != nil {
				return nil, err
			}
			tr := tar.NewReader(f)
			found := false
			for {
				h, er := tr.Next()
				if er == io.EOF {
					break
				}
				if er != nil {
					f.Close()
					return nil, er
				}
				if h.Name == "rootfs/payload" {
					b, er := io.ReadAll(tr)
					if er != nil {
						f.Close()
						return nil, er
					}
					if string(b) != string(r.expected) {
						f.Close()
						return nil, fmt.Errorf("received stream payload mismatch")
					}
					found = true
				}
			}
			f.Close()
			if !found {
				return nil, fmt.Errorf("native image missing received payload")
			}
			r.checked = true
		}
		return r.fakeRunner.Run(ctx, env, in, cmd, a...)
	}
	return (ExecRunner{}).Run(ctx, env, in, cmd, a...)
}
func (r *kernelRunner) RunStream(ctx context.Context, env []string, in io.Reader, out io.Writer, cmd string, a ...string) error {
	return (ExecRunner{}).RunStream(ctx, env, in, out, cmd, a...)
}
func TestRealStorageStreamRoundTrip(t *testing.T) {
	driver := os.Getenv("LXCER_TEST_STORAGE_DRIVER")
	parent := os.Getenv("LXCER_TEST_STREAM_PARENT")
	if os.Getenv("LXCER_TEST_DISPOSABLE") != "YES" {
		t.Skip("NOT VERIFIED: disposable integration was not enabled")
	}
	if (driver != "btrfs" && driver != "zfs") || !strings.Contains(parent, "lxcer-test-") {
		t.Fatal("NOT VERIFIED: requires explicit disposable Btrfs/ZFS parent with lxcer-test- in its name")
	}
	if driver == "btrfs" && !filepath.IsAbs(parent) {
		t.Fatal("Btrfs test parent must be absolute")
	}
	if driver == "zfs" && !datasetPattern.MatchString(parent) {
		t.Fatal("invalid ZFS test parent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	exec := ExecRunner{}
	id := newID()
	root := ""
	source := ""
	if driver == "btrfs" {
		root = filepath.Join(parent, id)
		source = root
		if _, err := exec.Run(ctx, nil, nil, "btrfs", "subvolume", "create", root); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, err := exec.Run(context.Background(), nil, nil, "btrfs", "subvolume", "delete", root)
			if err != nil {
				t.Error(err)
			}
		})
	} else {
		root = filepath.Join(t.TempDir(), "source")
		source = parent + "/" + id
		if _, err := exec.Run(ctx, nil, nil, "zfs", "create", "-o", "mountpoint="+root, "-o", "org.lxcer:test="+id, source); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			b, err := exec.Run(context.Background(), nil, nil, "zfs", "get", "-H", "-o", "value", "org.lxcer:test", source)
			if err != nil || strings.TrimSpace(string(b)) != id {
				t.Error("test dataset ownership check failed")
				return
			}
			if _, err = exec.Run(context.Background(), nil, nil, "zfs", "destroy", "-r", source); err != nil {
				t.Error(err)
			}
		})
	}
	data := make([]byte, 8<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payload"), data, 0600); err != nil {
		t.Fatal(err)
	}
	base := fake()
	base.instances["local"][1].Config["volatile.last_state.idmap"] = fmt.Sprintf(`[{"Isuid":true,"Hostid":%d,"Nsid":0,"Maprange":1},{"Isgid":true,"Hostid":%d,"Nsid":0,"Maprange":1}]`, os.Getuid(), os.Getgid())
	runner := &kernelRunner{fakeRunner: base, driver: driver}
	e := testEngine(t, base)
	e.R = runner
	if driver == "btrfs" {
		e.C.StateDir = filepath.Join(parent, id+"-state")
		if err := privateDir(e.C.StateDir); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, m := range manifests(t, e) {
			if m.Stream != nil && m.Stream.Driver == "btrfs" {
				if _, err := os.Lstat(m.Stream.Snapshot); err == nil {
					if _, err = exec.Run(context.Background(), nil, nil, "btrfs", "subvolume", "delete", m.Stream.Snapshot); err != nil {
						t.Error(err)
					}
				}
			}
		}
	})
	e.C.Policy = Policy{Strategy: "storage-incremental", StorageSource: source, NativeRoot: root}
	e.C.Destinations[0].Path = filepath.Join(t.TempDir(), "repository")
	e.C.Destinations[0].Password = "isolated-stream-test"
	if _, err := e.repo(e.C.Destinations[0]).run(ctx, "init"); err != nil {
		t.Fatal(err)
	}
	if err := e.Backup(ctx, []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	var full Manifest
	for _, m := range manifests(t, e) {
		full = m
	}
	copy(data[4096:8192], []byte(strings.Repeat("d", 4096)))
	changed, err := os.OpenFile(filepath.Join(root, "payload"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = changed.WriteAt(data[4096:8192], 4096)
	err = errors.Join(err, changed.Sync(), changed.Close())
	if err != nil {
		t.Fatal(err)
	}
	if driver == "zfs" {
		time.Sleep(1 * time.Second)
	}
	if err := e.Backup(ctx, []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	var delta Manifest
	for _, m := range manifests(t, e) {
		if len(m.Stream.Parents) == 1 {
			delta = m
		}
	}
	if delta.ID == "" {
		t.Fatal("missing delta manifest")
	}
	runner.expected = data
	if err := e.restore(ctx, &delta, RestoreOptions{Name: "stream-recovery", Host: "lab", Destination: "primary", Test: true, Isolated: true, Profile: "isolated", ReceiveDataset: parent}); err != nil {
		t.Fatal(err)
	}
	if !runner.checked || delta.RestoreTest == nil || !delta.RestoreTest.Success {
		t.Fatal("receive/native-image verification missing")
	}
	t.Logf("PASS real storage protocol driver=%s full_bytes=%d delta_bytes=%d delta_packed_upload_bytes=%d capture_seconds=%g restore_seconds=%g; LXD import modeled", driver, full.Size, delta.Size, delta.Destinations[0].UploadBytes, delta.CaptureSeconds, delta.RestoreTest.Duration)
	if delta.Size >= full.Size {
		t.Fatal("delta did not reduce stream bytes for a 4 KiB change")
	}
}

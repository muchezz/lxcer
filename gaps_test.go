package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type storageRunner struct {
	*fakeRunner
	driver, root string
	muStorage    sync.Mutex
	streams      []string
	deny         string
}

func (f *storageRunner) Run(ctx context.Context, env []string, in []byte, cmd string, a ...string) ([]byte, error) {
	if cmd == "tar" {
		return (ExecRunner{}).Run(ctx, env, in, cmd, a...)
	}
	if cmd == "lxc" && len(a) > 1 && a[0] == "query" && strings.Contains(a[1], "/storage-pools/") {
		return []byte(`{"driver":"` + f.driver + `"}`), nil
	}
	if cmd == "btrfs" || cmd == "zfs" {
		call := cmd + " " + strings.Join(a, " ")
		f.muStorage.Lock()
		f.streams = append(f.streams, call)
		f.muStorage.Unlock()
		if f.deny != "" && strings.Contains(call, f.deny) {
			return nil, fmt.Errorf("injected %s failure", cmd)
		}
		if cmd == "btrfs" {
			if a[0] == "property" {
				return []byte("ro=true\n"), nil
			}
			return nil, nil
		}
		if a[0] == "get" {
			if a[len(a)-2] == "mountpoint" {
				return []byte(f.root + "\n"), nil
			}
			return []byte("4096\n"), nil
		}
		if a[0] == "list" {
			if a[len(a)-1] == "tank/source" {
				return []byte("tank/source\n"), nil
			}
			return nil, os.ErrNotExist
		}
		return nil, nil
	}
	return f.fakeRunner.Run(ctx, env, in, cmd, a...)
}
func (f *storageRunner) RunStream(ctx context.Context, env []string, _ io.Reader, out io.Writer, cmd string, a ...string) error {
	if cmd == "restic" {
		b, err := f.fakeRunner.Run(ctx, env, nil, cmd, a...)
		if err != nil {
			return err
		}
		_, err = out.Write(b)
		return err
	}
	call := cmd + " " + strings.Join(a, " ")
	f.muStorage.Lock()
	f.streams = append(f.streams, call)
	f.muStorage.Unlock()
	if f.deny != "" && strings.Contains(call, f.deny) {
		return fmt.Errorf("injected send failure")
	}
	size := 32 << 10
	if strings.Contains(call, " -p ") || strings.Contains(call, " -i ") {
		size = 1024
	}
	if out != nil {
		_, err := out.Write(bytes.Repeat([]byte("s"), size))
		return err
	}
	return nil
}
func TestStorageIncrementalChains(t *testing.T) {
	for _, driver := range []string{"btrfs", "zfs"} {
		t.Run(driver, func(t *testing.T) {
			root := t.TempDir()
			f := &storageRunner{fakeRunner: fake(), driver: driver, root: root}
			e := testEngine(t, f.fakeRunner)
			e.R = f
			e.C.Policy = Policy{Strategy: "storage-incremental", StorageSource: root, NativeRoot: root, MaxChain: 3}
			if driver == "zfs" {
				e.C.Policy.StorageSource = "tank/source"
			}
			for i := 0; i < 3; i++ {
				if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
					t.Fatal(err)
				}
			}
			mm := manifests(t, e)
			byLen := map[int]Manifest{}
			for _, m := range mm {
				if m.Stream == nil || m.State != Verified {
					t.Fatalf("stream not verified: %+v", m)
				}
				byLen[len(m.Stream.Parents)] = m
			}
			if len(byLen) != 3 {
				t.Fatal("incremental parents not recorded")
			}
			if byLen[0].Size <= byLen[1].Size {
				t.Fatal("simulated delta was not smaller than full stream")
			}
			if len(byLen[2].Stream.Parents) != 2 {
				t.Fatal("chain incomplete")
			}
			calls := strings.Join(f.streams, "\n")
			if strings.Contains(calls, "lxc export") {
				t.Fatal("storage streams exported a full LXD archive")
			}
			flag := " -p "
			if driver == "zfs" {
				flag = " -i "
			}
			if !strings.Contains(calls, flag) {
				t.Fatal("storage send did not reference parent")
			}
			// Failed stream must not advance the last committed source checkpoint.
			before, err := os.ReadFile(e.checkpointPath("local", "stopped"))
			if err != nil {
				t.Fatal(err)
			}
			f.deny = "send"
			if err = e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err == nil {
				t.Fatal("incomplete stream succeeded")
			}
			after, _ := os.ReadFile(e.checkpointPath("local", "stopped"))
			if !bytes.Equal(before, after) {
				t.Fatal("failed stream advanced checkpoint")
			}
		})
	}
}
func TestIncrementalFallbackAndMissingParent(t *testing.T) {
	root := t.TempDir()
	f := &storageRunner{fakeRunner: fake(), driver: "lvm", root: root}
	e := testEngine(t, f.fakeRunner)
	e.R = f
	e.C.Policy = Policy{Strategy: "storage-incremental", IncrementalFallback: "full"}
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	m := manifests(t, e)[0]
	if m.Strategy != "full" || m.FallbackReason == "" {
		t.Fatal("fallback was not explicit and recorded")
	}
	f = &storageRunner{fakeRunner: fake(), driver: "btrfs", root: root}
	e = testEngine(t, f.fakeRunner)
	e.R = f
	e.C.Policy = Policy{Strategy: "storage-incremental", StorageSource: root, NativeRoot: root}
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	f.repos["test-primary"] = map[string][]string{}
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err == nil {
		t.Fatal("missing chain parent accepted")
	}
}
func TestChainRetentionKeepsAncestorsAndUnfinishedChains(t *testing.T) {
	makeRow := func(id, base string, hour int, confirmed bool) retainedSnapshot {
		return retainedSnapshot{ID: strings.Repeat(id, 64), Time: time.Unix(int64(hour)*3600, 0), Confirmed: confirmed, Tags: []string{"lxcer-data", "lxcer-chain", "chain=" + base, "job=job-" + id, "host=h", "instance=i"}}
	}
	rows := []retainedSnapshot{makeRow("a", "base-old", 1, true), makeRow("b", "base-old", 2, true), makeRow("c", "base-current", 3, true), makeRow("d", "base-current", 4, true), makeRow("e", "base-incomplete", 5, false)}
	ids, err := retentionIDs(rows, Destination{KeepLast: 1}, "h", "i")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != strings.Repeat("a", 64) || ids[1] != strings.Repeat("b", 64) {
		t.Fatalf("unsafe retention IDs %v", ids)
	}
}
func TestNativeImageContainsRootAndNumericOwners(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root-data")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sentinel"), []byte("native recovery"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sentinel", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	f := fake()
	e := testEngine(t, f)
	e.R = ExecRunner{}
	m := Manifest{ID: newID(), Instance: instance("native", 102), Started: time.Now()}
	m.Instance.Config["volatile.last_state.idmap"] = fmt.Sprintf(`[{"Isuid":true,"Hostid":%d,"Nsid":0,"Maprange":1},{"Isgid":true,"Hostid":%d,"Nsid":0,"Maprange":1}]`, os.Getuid(), os.Getgid())

	image, err := e.nativeImage(context.Background(), root, dir, m)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(image)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	tr := tar.NewReader(file)
	found := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		found[strings.TrimSuffix(h.Name, "/")] = true
		if h.Uid != 0 || h.Gid != 0 {
			t.Fatalf("shifted owner leaked: %+v", h)
		}
		if h.Name == "rootfs/link" && h.Linkname != "sentinel" {
			t.Fatal("symlink target transformed")
		}
	}
	if !found["metadata.yaml"] || !found["rootfs/sentinel"] {
		t.Fatalf("invalid LXD image members %v", found)
	}
}
func TestStagingBudgetCancelsAndRetains(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	e.C.MaxStagingBytes = 4096
	e.R = runnerFunc(func(ctx context.Context, env []string, in []byte, cmd string, a ...string) ([]byte, error) {
		if cmd == "lxc" && a[0] == "export" && len(a) > 2 {
			return nil, os.WriteFile(a[2], bytes.Repeat([]byte("x"), 8192), 0600)
		}
		return f.Run(ctx, env, in, cmd, a...)
	})
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err == nil {
		t.Fatal("over-budget export succeeded")
	}
	m := manifests(t, e)[0]
	if m.State != Failed || m.Cleanup == "completed" {
		t.Fatal("over-budget job cleaned")
	}
	if _, err := os.Stat(m.Artifact); err != nil {
		t.Fatal("only recovery copy removed")
	}
	if strings.Contains(strings.Join(f.calls, "\n"), "restic backup") {
		t.Fatal("budget failure uploaded data")
	}
}
func TestLargeTreeVerificationIsBounded(t *testing.T) {
	w := &nodeFinder{path: "/expected"}
	row := []byte(`{"struct_type":"node","path":"/other"}` + "\n")
	for i := 0; i < 300000; i++ {
		if _, err := w.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(map[string]string{"struct_type": "node", "path": "/expected"})
	if _, err := w.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	if !w.found || len(w.pending) > 1<<20 {
		t.Fatal("tree verification buffering incorrect")
	}
}

func TestShiftedACLAndCapabilityMetadata(t *testing.T) {
	ranges := []idRange{{Isuid: true, Hostid: 100000, Nsid: 0, Maprange: 65536}, {Isgid: true, Hostid: 200000, Nsid: 0, Maprange: 65536}}
	acl := make([]byte, 20)
	binary.LittleEndian.PutUint32(acl[:4], 2)
	binary.LittleEndian.PutUint16(acl[4:6], 2)
	binary.LittleEndian.PutUint32(acl[8:12], 100042)
	binary.LittleEndian.PutUint16(acl[12:14], 8)
	binary.LittleEndian.PutUint32(acl[16:20], 200043)
	mapped, changed, err := unshiftXattr("system.posix_acl_access", acl, ranges)
	if err != nil || !changed || binary.LittleEndian.Uint32(mapped[8:12]) != 42 || binary.LittleEndian.Uint32(mapped[16:20]) != 43 {
		t.Fatalf("ACL mapping failed %v", err)
	}
	cap := make([]byte, 24)
	binary.LittleEndian.PutUint32(cap[:4], 0x03000001)
	binary.LittleEndian.PutUint32(cap[20:24], 100000)
	mapped, changed, err = unshiftXattr("security.capability", cap, ranges)
	if err != nil || !changed || binary.LittleEndian.Uint32(mapped[20:24]) != 0 {
		t.Fatal("capability root mapping failed")
	}
}
func TestNativeRootCannotEscapeStaging(t *testing.T) {
	stage := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(stage, "escape")); err != nil {
		t.Fatal(err)
	}
	e := testEngine(t, fake())
	m := Manifest{ID: newID(), Started: time.Now(), Instance: instance("test", 102)}
	if _, err := e.nativeImage(context.Background(), filepath.Join(stage, "escape"), stage, m); err == nil {
		t.Fatal("escaped reconstruction root accepted")
	}
}

func TestPoolPreflightRejectsExhaustion(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	e.C.MinFreeBytes = 1024
	e.C.RequirePoolSpace = true
	r := runnerFunc(func(_ context.Context, _ []string, _ []byte, _ string, a ...string) ([]byte, error) {
		return []byte(`{"space":{"total":4096,"used":4000}}`), nil
	})
	m := Manifest{}
	if err := e.poolPreflight(context.Background(), LXD{R: r}, instance("app", 102), &m); err == nil {
		t.Fatal("full source pool accepted")
	}
	if m.PoolFreeBytes != 96 {
		t.Fatal("pool capacity not recorded")
	}
}
func TestChainReceiveRejectsCorruptStream(t *testing.T) {
	root := t.TempDir()
	f := &storageRunner{fakeRunner: fake(), driver: "btrfs", root: root}
	e := testEngine(t, f.fakeRunner)
	e.R = f
	e.C.Policy = Policy{Strategy: "storage-incremental", StorageSource: root, NativeRoot: root}
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	m := manifests(t, e)[0]
	m.SHA256 = strings.Repeat("0", 64)
	_, release, err := e.receiveStream(context.Background(), e.repo(e.C.Destinations[0]), m.Destinations[0].SnapshotID, t.TempDir(), m, RestoreOptions{})
	if err == nil {
		t.Fatal("corrupt stream received")
	}
	if releaseErr := release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	for _, call := range f.streams {
		if strings.Contains(call, "receive") {
			t.Fatal("unverified stream reached storage driver")
		}
	}
}
func TestPartialCheckpointRequiresAllVerifiedReceipts(t *testing.T) {
	id := strings.Repeat("a", 64)
	m := Manifest{State: PartiallyCompleted, Destinations: []DestinationStatus{{Required: true, Verified: true, SnapshotID: id, ReceiptID: id}}}
	if !recoverableCheckpoint(m) {
		t.Fatal("cleanup-only failure invalidated recoverable checkpoint")
	}
	m.Destinations[0].ReceiptID = ""
	if recoverableCheckpoint(m) {
		t.Fatal("incomplete receipt allowed as checkpoint")
	}
}

func TestMissingParentCanExplicitlyRestartFullStream(t *testing.T) {
	root := t.TempDir()
	f := &storageRunner{fakeRunner: fake(), driver: "btrfs", root: root}
	e := testEngine(t, f.fakeRunner)
	e.R = f
	e.C.Policy = Policy{Strategy: "storage-incremental", StorageSource: root, NativeRoot: root, IncrementalFallback: "full"}
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	f.repos["test-primary"] = map[string][]string{}
	if err := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range manifests(t, e) {
		if m.FallbackReason != "" {
			found = true
			if len(m.Stream.Parents) != 0 || m.Stream.Mode != "full" || m.Stream.BaseID != m.ID {
				t.Fatal("full restart kept broken dependencies")
			}
		}
	}
	if !found {
		t.Fatal("explicit fallback was not reported")
	}
}

func TestZFSWithLegacyLXDMountpoint(t *testing.T) {
	root := t.TempDir()
	e := testEngine(t, fake())
	e.R = runnerFunc(func(_ context.Context, _ []string, _ []byte, cmd string, a ...string) ([]byte, error) {
		if cmd == "findmnt" {
			return json.Marshal(map[string]any{"filesystems": []any{map[string]string{"target": root, "fsroot": "/"}}})
		}
		if a[0] == "get" {
			return []byte("legacy\n"), nil
		}
		return []byte("tank/source\n"), nil
	})
	got, err := e.streamRoot(context.Background(), Policy{StorageSource: "tank/source", NativeRoot: filepath.Join(root, "rootfs")}, "zfs")
	if err != nil || got != root {
		t.Fatalf("legacy ZFS mount was not resolved: %q %v", got, err)
	}
}

func TestStreamCannotCaptureItsOwnState(t *testing.T) {
	root := t.TempDir()
	f := &storageRunner{fakeRunner: fake(), driver: "btrfs", root: root}
	e := testEngine(t, f.fakeRunner)
	e.R = f
	e.C.StateDir = filepath.Join(root, "state")
	p := Policy{Strategy: "storage-incremental", StorageSource: root, NativeRoot: root}
	if err := e.streamPreflight(context.Background(), LXD{R: f, Host: "local"}, instance("test", 102), p, "btrfs"); err == nil {
		t.Fatal("recursive self-backup accepted")
	}
	for _, call := range f.streams {
		if strings.Contains(call, "snapshot -r") {
			t.Fatal("source was snapshotted despite invalid state path")
		}
	}
}

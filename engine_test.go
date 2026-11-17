package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	data       map[string]map[string][]byte
	mu         sync.Mutex
	instances  map[string][]Container
	snapshots  map[string][]string
	repos      map[string]map[string][]string
	calls      []string
	fail       string
	realRestic bool
	seq        int
	active     int
	max        int
	delay      time.Duration
}

func instance(n string, state int) Container {
	return Container{Name: n, StatusCode: state, Type: "container", Architecture: "x86_64", Config: map[string]string{}, ExpandedDevices: map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}}}
}
func fake() *fakeRunner {
	return &fakeRunner{instances: map[string][]Container{"local": {instance("running", 103), instance("stopped", 102)}}, snapshots: map[string][]string{}, repos: map[string]map[string][]string{}, data: map[string]map[string][]byte{}}
}
func splitRef(s string) (string, string) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return "local", s
	}
	return a, b
}
func jsonBytes(v any) ([]byte, error) { return json.Marshal(v) }
func (f *fakeRunner) Run(ctx context.Context, env []string, in []byte, cmd string, a ...string) ([]byte, error) {
	if (cmd == "restic" || cmd == "zstd") && f.realRestic {
		return (ExecRunner{}).Run(ctx, env, in, cmd, a...)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	call := cmd + " " + strings.Join(a, " ")
	f.calls = append(f.calls, call)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if f.fail != "" && strings.Contains(call, f.fail) {
		return nil, errors.New("injected failure")
	}
	if cmd == "restic" {
		repo := ""
		for _, v := range env {
			if strings.HasPrefix(v, "RESTIC_REPOSITORY=") {
				repo = strings.TrimPrefix(v, "RESTIC_REPOSITORY=")
			}
		}
		if f.repos[repo] == nil {
			f.repos[repo] = map[string][]string{}
		}
		switch a[0] {
		case "backup":
			idx := 0
			for i, v := range a {
				if v == "--" {
					idx = i + 1
				}
			}
			f.seq++
			id := strings.Repeat("a", 56) + strings.ReplaceAll(time.Unix(int64(f.seq), 0).UTC().Format("150405"), ":", "") + "aa"
			f.repos[repo][id] = append([]string{}, a[idx:]...)
			f.data[id] = map[string][]byte{}
			for _, path := range a[idx:] {
				if b, er := os.ReadFile(path); er == nil {
					f.data[id][path] = b
				}
			}
			return jsonBytes(map[string]string{"message_type": "summary", "snapshot_id": id})
		case "snapshots":
			id := a[len(a)-1]
			paths, ok := f.repos[repo][id]
			if !ok {
				if id == "--json" {
					var rows []any
					for snap, paths := range f.repos[repo] {
						rows = append(rows, map[string]any{"id": snap, "paths": paths, "time": time.Unix(1600000000, 0).UTC()})
					}
					return jsonBytes(rows)
				}
				return []byte("[]"), nil
			}
			return jsonBytes([]any{map[string]any{"id": id, "paths": paths, "time": time.Unix(1600000000, 0).UTC()}})
		case "ls":
			return jsonBytes(map[string]string{"struct_type": "node", "path": a[len(a)-1]})
		case "restore":
			target := ""
			include := ""
			for i, v := range a {
				if v == "--target" {
					target = a[i+1]
				}
				if v == "--include" {
					include = a[i+1]
				}
			}
			for path, b := range f.data[a[1]] {
				if include != "" && path != include {
					continue
				}
				dest := filepath.Join(target, strings.TrimPrefix(path, "/"))
				if er := os.MkdirAll(filepath.Dir(dest), 0700); er != nil {
					return nil, er
				}
				if er := os.WriteFile(dest, b, 0600); er != nil {
					return nil, er
				}
			}
			return nil, nil
		case "check", "forget":
			return nil, nil
		}
	}
	if cmd == "zstd" {
		return nil, errors.New("fake compressor not enabled")
	}
	if cmd != "lxc" {
		return nil, errors.New("unexpected executable")
	}
	switch a[0] {
	case "storage":
		if a[2] == "export" {
			return nil, os.WriteFile(a[5], []byte("volume data"), 0600)
		}
		if a[2] == "list" {
			return []byte("[]"), nil
		}
		return nil, nil
	case "query":
		host, path := splitRef(a[1])
		switch {
		case path == "/1.0":
			return []byte(`{"environment":{"server_version":"6.0"},"api_extensions":["container_backup","instances","container_incremental_copy","backup_override_name","container_backup_override_pool","custom_volume_backup"]}`), nil
		case strings.HasPrefix(path, "/1.0/images/aliases/"):
			return []byte(`{"target":"` + strings.Repeat("f", 64) + `"}`), nil
		case strings.HasPrefix(path, "/1.0/storage-pools/"):
			return []byte(`{"driver":"dir"}`), nil
		case strings.HasPrefix(path, "/1.0/profiles/"):
			return []byte(`{"devices":{"root":{"type":"disk","path":"/","pool":"default"}}}`), nil
		case strings.HasSuffix(path, "/snapshots"):
			return jsonBytes(f.snapshots[host])
		}
	case "version":
		return []byte("Client version: 6.0"), nil
	case "export":
		if a[1] == "--help" {
			return []byte("export --compression"), nil
		}
		f.active++
		if f.active > f.max {
			f.max = f.active
		}
		f.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(f.delay):
		}
		f.mu.Lock()
		f.active--
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, os.WriteFile(a[2], []byte("recoverable filesystem archive"), 0600)
	case "copy":
		if a[1] == "--help" {
			return []byte("copy --refresh"), nil
		}
		sh, sn := splitRef(a[1])
		dh, dn := splitRef(a[2])
		var copied Container
		for _, c := range f.instances[sh] {
			if c.Name == sn {
				copied = c
			}
		}
		copied.Name = dn
		copied.StatusCode = 102
		cfg := map[string]string{}
		for k, v := range copied.Config {
			cfg[k] = v
		}
		copied.Config = cfg
		for i, v := range a {
			if v == "-c" {
				k, value, _ := strings.Cut(a[i+1], "=")
				copied.Config[k] = value
			}
		}
		found := false
		for i, c := range f.instances[dh] {
			if c.Name == dn {
				f.instances[dh][i] = copied
				found = true
			}
		}
		if !found {
			f.instances[dh] = append(f.instances[dh], copied)
		}
		return nil, nil
	case "list":
		host, _ := splitRef(a[1])
		return jsonBytes(f.instances[host])
	case "snapshot":
		host, n := splitRef(a[1])
		f.snapshots[host] = append(f.snapshots[host], "/1.0/containers/"+n+"/snapshots/"+a[2])
		return nil, nil
	case "publish":
		return nil, nil
	case "image":
		switch a[1] {
		case "export":
			return nil, os.WriteFile(a[3]+".tar", []byte("legacy image archive"), 0600)
		case "import":
			return nil, nil
		case "alias":
			if a[2] == "list" {
				return []byte("[]"), nil
			}
			return nil, nil
		}
	case "import":
		if len(a) > 1 && a[1] == "--help" {
			return []byte("[<remote>:] <backup file> [<instance name>] --storage"), nil
		}
		if len(a) < 4 || !strings.HasSuffix(a[1], ":") {
			return nil, errors.New("invalid import argument order")
		}
		h, _ := splitRef(a[1])
		f.instances[h] = append(f.instances[h], instance(a[3], 102))
		return nil, nil
	case "init":
		host, n := splitRef(a[2])
		c := instance(n, 102)
		for i, v := range a {
			if v == "-c" {
				k, value, _ := strings.Cut(a[i+1], "=")
				c.Config[k] = value
			}
		}
		f.instances[host] = append(f.instances[host], c)
		return nil, nil
	case "config":
		if a[1] == "set" {
			h, n := splitRef(a[2])
			for i, c := range f.instances[h] {
				if c.Name == n {
					f.instances[h][i].Config[a[3]] = a[4]
				}
			}
		}
		if a[1] == "edit" {
			h, n := splitRef(a[2])
			var cfg struct {
				Config  map[string]string            `json:"config"`
				Devices map[string]map[string]string `json:"devices"`
			}
			if er := json.Unmarshal(in, &cfg); er != nil {
				return nil, er
			}
			for i, c := range f.instances[h] {
				if c.Name == n {
					f.instances[h][i].Config = cfg.Config
					f.instances[h][i].ExpandedDevices = cfg.Devices
				}
			}
		}
		return nil, nil
	case "move":
		h, n := splitRef(a[1])
		dh, dn := splitRef(a[2])
		if h != dh {
			return nil, errors.New("fake only models same-host rename")
		}
		for i, c := range f.instances[h] {
			if c.Name == n {
				f.instances[h][i].Name = dn
				return nil, nil
			}
		}
		return nil, errors.New("rename source missing")
	case "delete":
		host, n := splitRef(a[1])
		var cc []Container
		for _, c := range f.instances[host] {
			if c.Name != n {
				cc = append(cc, c)
			}
		}
		f.instances[host] = cc
		return nil, nil
	case "exec", "start":
		return nil, nil
	}
	return nil, errors.New("unexpected command: " + call)
}
func testEngine(t *testing.T, f *fakeRunner) Engine {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	return Engine{C: Config{Version: 1, StateDir: root, Timeout: time.Minute, Workers: 4, Compression: "none", Policy: Policy{Strategy: "auto"}, Destinations: []Destination{{Name: "primary", Path: "test-primary"}}}, R: f, Log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
}
func manifests(t *testing.T, e Engine) []Manifest {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(e.C.StateDir, "lxcer-*", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []Manifest
	for _, f := range files {
		m, err := loadManifest(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}
func TestBackupRunningStoppedAndSelected(t *testing.T) {
	for _, name := range []string{"", "stopped", "missing"} {
		t.Run(name, func(t *testing.T) {
			f := fake()
			e := testEngine(t, f)
			err := e.Backup(context.Background(), []Host{{Name: "local", Workers: 2}}, name)
			if name == "missing" {
				if err == nil {
					t.Fatal("missing container succeeded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			mm := manifests(t, e)
			want := 2
			if name != "" {
				want = 1
			}
			if len(mm) != want {
				t.Fatalf("jobs=%d", len(mm))
			}
			for _, m := range mm {
				if m.State != Verified || m.Cleanup != "completed" || m.Destinations[0].ReceiptID == "" {
					t.Fatalf("bad manifest %+v", m)
				}
				if _, er := os.Stat(m.Artifact); !os.IsNotExist(er) {
					t.Fatal("artifact not cleaned")
				}
			}
		})
	}
}
func TestBackupFailurePreservesArtifacts(t *testing.T) {
	for _, tc := range []struct{ name, fail, strategy, compression string }{{"snapshot", "lxc snapshot", "snapshot", "none"}, {"export", "lxc export local:stopped", "full", "none"}, {"upload", "restic backup", "full", "none"}, {"verification", "restic ls", "full", "none"}, {"compression", "zstd", "full", "zstd"}, {"receipt", "restic backup --json --tag lxcer-receipt", "full", "none"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake()
			f.fail = tc.fail
			e := testEngine(t, f)
			e.C.Policy.Strategy = tc.strategy
			e.C.Compression = tc.compression
			if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er == nil {
				t.Fatal("failure reported success")
			}
			m := manifests(t, e)[0]
			if m.State != Failed && m.State != PartiallyCompleted {
				t.Fatalf("state=%s", m.State)
			}
			if m.Cleanup == "completed" {
				t.Fatal("cleaned failed job")
			}
			if tc.name == "upload" || tc.name == "verification" || tc.name == "receipt" {
				if _, er := os.Stat(m.Artifact); er != nil {
					t.Fatal(er)
				}
			}
		})
	}
}
func TestDestinationsAndPartialSuccess(t *testing.T) {
	for _, optional := range []bool{false, true} {
		f := fake()
		e := testEngine(t, f)
		e.C.Destinations = append(e.C.Destinations, Destination{Name: "other", Path: "other", Optional: optional})
		f.fail = ""
		r := &failDestination{Runner: f, path: "other"}
		e.R = r
		if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er == nil {
			t.Fatal("partial reported complete")
		}
		m := manifests(t, e)[0]
		if m.State != PartiallyCompleted || !m.Destinations[0].Verified || m.Destinations[1].Verified {
			t.Fatalf("bad partial %+v", m)
		}
		if _, er := os.Stat(m.Artifact); er != nil {
			t.Fatal(er)
		}
	}
}

type failDestination struct {
	Runner
	path string
}

func (r *failDestination) Run(c context.Context, env []string, b []byte, cmd string, a ...string) ([]byte, error) {
	for _, v := range env {
		if v == "RESTIC_REPOSITORY="+r.path {
			return nil, errors.New("injected destination failure")
		}
	}
	return r.Runner.Run(c, env, b, cmd, a...)
}
func TestQuiesceFailureAlwaysResumes(t *testing.T) {
	f := fake()
	f.fail = "freeze"
	e := testEngine(t, f)
	e.C.Policy.Hooks = Hooks{Quiesce: []Hook{{Argv: []string{"freeze"}}}, Resume: []Hook{{Argv: []string{"resume"}}}}
	if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "running"); er == nil {
		t.Fatal("quiesce failure accepted")
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "-- resume") {
		t.Fatal("resume not attempted")
	}
	m := manifests(t, e)[0]
	if m.NeedsResume {
		t.Fatal("successful resume not persisted")
	}
}
func TestDiskExhaustionAndExcludedVolume(t *testing.T) {
	for _, diskFull := range []bool{false, true} {
		f := fake()
		e := testEngine(t, f)
		if diskFull {
			e.C.MinFreeBytes = ^uint64(0)
		} else {
			f.instances["local"][1].ExpandedDevices["data"] = map[string]string{"type": "disk", "source": "/srv/data", "path": "/data"}
		}
		if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er == nil {
			t.Fatal("unsafe preflight accepted")
		}
		if strings.Contains(strings.Join(f.calls, "\n"), "lxc export local:stopped ") {
			t.Fatal("export ran despite preflight failure")
		}
	}
}
func TestConcurrencyAndLocks(t *testing.T) {
	f := fake()
	f.delay = 30 * time.Millisecond
	e := testEngine(t, f)
	e.C.Workers = 2
	f.instances["local"] = nil
	for _, n := range []string{"one", "two", "three", "four"} {
		f.instances["local"] = append(f.instances["local"], instance(n, 102))
	}
	if er := e.Backup(context.Background(), []Host{{Name: "local", Workers: 3}}, ""); er != nil {
		t.Fatal(er)
	}
	if f.max != 2 {
		t.Fatalf("max exports %d", f.max)
	}
	lock, er := acquireLock(e.C.StateDir, "same")
	if er != nil {
		t.Fatal(er)
	}
	if f2, er := acquireLock(e.C.StateDir, "same"); er == nil {
		releaseLock(f2)
		t.Fatal("duplicate lock acquired")
	}
	releaseLock(lock)
	f2, er := acquireLock(e.C.StateDir, "same")
	if er != nil {
		t.Fatal(er)
	}
	releaseLock(f2)
}
func TestNativeRequiresLocalPathsAndConsistency(t *testing.T) {
	caps := Capabilities{}
	if _, er := chooseStrategy(Policy{Strategy: "native", Paths: []string{"/tmp/data"}}, caps, instance("run", 103)); er == nil {
		t.Fatal("missing root accepted")
	}
	root := t.TempDir()
	p := Policy{Strategy: "native", NativeRoot: root, Paths: []string{root}}
	if _, er := chooseStrategy(p, caps, instance("run", 103)); er == nil {
		t.Fatal("running native accepted without quiesce")
	}
	f := fake()
	e := testEngine(t, f)
	e.C.Policy = p
	if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er != nil {
		t.Fatal(er)
	}
}
func TestRealResticBackupVerifyRestore(t *testing.T) {
	if testing.Short() {
		t.Skip("real Restic integration")
	}
	if _, er := exec.LookPath("restic"); er != nil {
		t.Skip("restic unavailable")
	}
	for _, strategy := range []string{"full", "snapshot", "native"} {
		t.Run(strategy, func(t *testing.T) {
			f := fake()
			f.realRestic = true
			e := testEngine(t, f)
			e.C.Policy.Strategy = strategy
			if strategy == "full" {
				if _, er := exec.LookPath("zstd"); er != nil {
					t.Skip("zstd unavailable")
				}
				e.C.Compression = "zstd"
			}
			e.C.Destinations[0].Path = filepath.Join(t.TempDir(), "repository")
			e.C.Destinations[0].Password = "isolated-test-password"
			e.C.Destinations[0].ReadData = true
			r := e.repo(e.C.Destinations[0])
			if _, er := r.run(context.Background(), "init"); er != nil {
				t.Fatal(er)
			}
			if strategy == "native" {
				root := t.TempDir()
				if er := os.WriteFile(filepath.Join(root, "data"), []byte("test data"), 0600); er != nil {
					t.Fatal(er)
				}
				e.C.Policy.NativeRoot = root
				e.C.Policy.Paths = []string{root}
			}
			if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er != nil {
				t.Fatal(er)
			}
			m := manifests(t, e)[0]
			o := RestoreOptions{Name: "restored", Host: "lab", Destination: "primary", Test: true, Isolated: true, Profile: "isolated", Health: []string{"true"}}
			if strategy == "native" {
				o.Test = false
				o.NativeTarget = filepath.Join(t.TempDir(), "restore")
			}
			if er := e.restore(context.Background(), &m, o); er != nil {
				t.Fatal(er)
			}
			if strategy != "native" && (m.RestoreTest == nil || !m.RestoreTest.Success) {
				t.Fatal("restore test result missing")
			}
			if len(f.instances["lab"]) != 0 {
				t.Fatal("test instance not deleted")
			}
			if _, er := r.run(context.Background(), "check", "--read-data"); er != nil {
				t.Fatal(er)
			}
		})
	}
}

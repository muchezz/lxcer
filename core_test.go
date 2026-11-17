package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runnerFunc func(context.Context, []string, []byte, string, ...string) ([]byte, error)

func (f runnerFunc) Run(c context.Context, e []string, b []byte, n string, a ...string) ([]byte, error) {
	return f(c, e, b, n, a...)
}
func TestVersionCapabilityMatrix(t *testing.T) {
	for _, v := range []string{"3.0.3", "4.0", "5.0", "5.21", "6.0", "future"} {
		for _, modern := range []bool{false, true} {
			t.Run(v+map[bool]string{true: "-modern", false: "-legacy"}[modern], func(t *testing.T) {
				r := runnerFunc(func(_ context.Context, _ []string, _ []byte, _ string, a ...string) ([]byte, error) {
					switch a[0] {
					case "query":
						ext := `[]`
						if modern {
							ext = `["container_backup","instances","container_incremental_copy"]`
						}
						return []byte(`{"metadata":{"environment":{"server_version":"` + v + `"},"api_extensions":` + ext + `}}`), nil
					case "version":
						return []byte("client " + v), nil
					case "export":
						if !modern {
							return nil, errors.New("no export")
						}
						return []byte("export"), nil
					case "copy":
						return []byte("--refresh"), nil
					}
					return nil, errors.New("unexpected")
				})
				l := LXD{R: r}
				if er := l.Detect(context.Background()); er != nil {
					t.Fatal(er)
				}
				if l.Caps.Export != modern || l.Caps.Refresh != modern || l.Caps.Server != v {
					t.Fatalf("bad caps %+v", l.Caps)
				}
				s, er := chooseStrategy(Policy{Strategy: "auto"}, l.Caps, Container{})
				if er != nil {
					t.Fatal(er)
				}
				want := "snapshot"
				if modern {
					want = "full"
				}
				if s != want {
					t.Fatalf("strategy %s", s)
				}
			})
		}
	}
}
func TestStorageDrivers(t *testing.T) {
	for _, driver := range []string{"btrfs", "zfs", "lvm", "dir", "ceph"} {
		r := runnerFunc(func(context.Context, []string, []byte, string, ...string) ([]byte, error) {
			return []byte(`{"driver":"` + driver + `"}`), nil
		})
		got, er := (LXD{R: r}).Storage(context.Background(), instance("one", 102))
		if er != nil || got != driver {
			t.Fatalf("driver=%s err=%v", got, er)
		}
	}
}
func TestSelection(t *testing.T) {
	cc := []Container{instance("a", 102), instance("b", 103)}
	for _, tc := range []struct {
		name  string
		black []string
		want  int
		fail  bool
	}{{"", nil, 2, false}, {"a", nil, 1, false}, {"missing", nil, 0, true}, {"a", []string{"a"}, 0, true}, {"", []string{"a", "b"}, 0, true}, {"../a", nil, 0, true}} {
		got, er := selectContainers(cc, tc.black, tc.name)
		if (er != nil) != tc.fail || len(got) != tc.want {
			t.Fatalf("%+v got %v %v", tc, got, er)
		}
	}
}
func TestTransitions(t *testing.T) {
	m := Manifest{State: Pending}
	for _, s := range []State{Running, SnapshotCreated, Exported, Uploaded, Verified} {
		if er := m.transition(s); er != nil {
			t.Fatal(er)
		}
	}
	if er := m.transition(Running); er == nil {
		t.Fatal("terminal job restarted")
	}
	m.State = Pending
	if er := m.transition(Verified); er == nil {
		t.Fatal("unverified jump allowed")
	}
}
func TestConfigLegacyAndValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.yml")
	for _, tc := range []struct {
		yaml string
		fail bool
	}{{"hosts: [old]\nlocal_workers: 2\nbackup_restic_repos:\n - path: /tmp/repo\n   password: legacy\n", false}, {"version: 99\n", true}, {"workers: 0\n", true}, {"unknown: true\n", true}, {"compression: dangerous\n", true}, {"inventory:\n - name: ../host\n", true}, {"policy:\n strategy: native\n paths: [/]\n", true}, {"policy:\n hooks:\n  quiesce:\n   - argv: [freeze]\n", true}, {"destinations:\n - name: one\n   path: /repo\n   password: p\n   password_env: PASS\n", true}} {
		if er := os.WriteFile(path, []byte(tc.yaml), 0600); er != nil {
			t.Fatal(er)
		}
		c, er := readConfig(path)
		if (er != nil) != tc.fail {
			t.Fatalf("yaml %s got %v", tc.yaml, er)
		}
		if !tc.fail && (c.Workers != 2 || len(c.Destinations) != 1) {
			t.Fatal("legacy not migrated")
		}
	}
}
func TestCleanupOwnershipAndUnverifiedProtection(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	m := Manifest{ID: "lxcer-owned", State: Failed}
	if er := e.cleanup(context.Background(), &m, filepath.Join(e.C.StateDir, m.ID), false); er == nil {
		t.Fatal("failed job cleaned")
	}
	m.State = Verified
	if er := e.cleanup(context.Background(), &m, "/tmp/unrelated", true); er == nil {
		t.Fatal("ownership ignored")
	}
	if er := e.cleanup(context.Background(), &m, filepath.Join(e.C.StateDir, m.ID), true); er == nil {
		t.Fatal("missing repositories ignored")
	}
}
func TestInterruptedRecovery(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	id := newID()
	dir := filepath.Join(e.C.StateDir, id)
	if er := privateDir(dir); er != nil {
		t.Fatal(er)
	}
	m := Manifest{Version: 1, ID: id, Host: "local", Instance: instance("stopped", 102), State: Running}
	if er := m.save(dir); er != nil {
		t.Fatal(er)
	}
	if er := e.recoverJobs(context.Background(), false); er != nil {
		t.Fatal(er)
	}
	if manifests(t, e)[0].State != Running {
		t.Fatal("preview mutated state")
	}
	if er := e.recoverJobs(context.Background(), true); er != nil {
		t.Fatal(er)
	}
	if manifests(t, e)[0].State != Failed {
		t.Fatal("interrupted not recorded")
	}
}
func TestSubprocessCancellationAndSecretSuppression(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, er := (ExecRunner{}).Run(ctx, nil, nil, "sleep", "10"); er == nil {
		t.Fatal("timeout ignored")
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout too slow")
	}
	_, er := (ExecRunner{}).Run(context.Background(), nil, nil, "sh", "-c", "echo secret >&2; exit 1")
	if er == nil || strings.Contains(er.Error(), "echo") || strings.Contains(er.Error(), "secret\n") {
		t.Fatal("output leaked")
	}
}
func TestCLINonzeroAndVersion(t *testing.T) {
	var b bytes.Buffer
	if code := run([]string{"version"}, &b, &b); code != 0 || !strings.Contains(b.String(), "lxcer") {
		t.Fatal("version")
	}
	b.Reset()
	if code := run([]string{"-config", "/does/not/exist", "-a", "backup"}, &b, &b); code == 0 {
		t.Fatal("invalid config succeeded")
	}
	if code := run([]string{"-cleanup"}, &b, &b); code == 0 {
		t.Fatal("unsafe cleanup accepted")
	}
}
func TestScopedRetention(t *testing.T) {
	var captured string
	r := Repository{D: Destination{Name: "d", Path: "r", KeepLast: 3}, R: runnerFunc(func(_ context.Context, _ []string, _ []byte, _ string, a ...string) ([]byte, error) {
		if a[0] == "snapshots" {
			return []byte("[]"), nil
		}
		captured = strings.Join(a, " ")
		return nil, nil
	})}
	if er := r.retention(context.Background(), "h", "c", true); er != nil {
		t.Fatal(er)
	}
	if !strings.Contains(captured, "--dry-run") || !strings.Contains(captured, "lxcer-data,host=h,instance=c") || strings.Contains(captured, "--prune") {
		t.Fatal(captured)
	}
}
func TestMetricsDurable(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	e.C.MetricsFile = filepath.Join(t.TempDir(), "lxcer.prom")
	if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er != nil {
		t.Fatal(er)
	}

	if info, er := os.Stat(e.C.MetricsFile); er != nil || info.Mode().Perm() != 0644 {
		t.Fatal("metrics file must be readable by node_exporter")
	}
	b, er := os.ReadFile(e.C.MetricsFile)
	if er != nil {
		t.Fatal(er)
	}
	if !strings.Contains(string(b), `lxcer_backup_success{host="local",instance="stopped"} 1`) {
		t.Fatal(string(b))
	}
}

func TestIncrementalMirrorRefreshAndCollision(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	e.C.Policy = Policy{Strategy: "incremental", Mirror: "mirror"}
	for i := 0; i < 2; i++ {
		if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er != nil {
			t.Fatal(er)
		}
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "--refresh --mode relay") {
		t.Fatal("no refresh on second backup")
	}
	mirror := f.instances["mirror"][0]
	if mirror.StatusCode != 102 || mirror.Config["boot.autostart"] != "false" || mirror.Config["user.lxcer.source"] != "local/stopped" {
		t.Fatal("unsafe mirror")
	}
	mirror.Config["user.lxcer.source"] = "unrelated"
	f.instances["mirror"][0] = mirror
	if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er == nil {
		t.Fatal("mirror collision accepted")
	}
}
func TestSSHAgentCommandQuotingAndFailure(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	e.C.Destinations = nil
	path := "/etc/lxcer/a'$(touch nope).yml"
	if er := e.Backup(context.Background(), []Host{{Name: "source", SSH: "source-agent", AgentBinary: "/opt/lxcer/new binary", RemoteConfig: path}}, "stopped"); er == nil {
		t.Fatal("failed agent reported success")
	}
	call := strings.Join(f.calls, "\n")
	if !strings.Contains(call, shellQuote("/opt/lxcer/new binary")) || !strings.Contains(call, "version >/dev/null &&") {
		t.Fatal("agent relied on production PATH or skipped version preflight")
	}
	if !strings.Contains(call, "StrictHostKeyChecking=yes") || !strings.Contains(call, shellQuote(path)) {
		t.Fatal(call)
	}
	if shellQuote("a'b") != "'a'\\''b'" {
		t.Fatal("incorrect quoting")
	}
}
func TestCredentialSourcesAndPermissions(t *testing.T) {
	r := Repository{D: Destination{Name: "one", Path: "local", PasswordEnv: "LXCER_TEST_PASSWORD"}}
	t.Setenv("LXCER_TEST_PASSWORD", "test-secret")
	env, er := r.env()
	if er != nil || !strings.Contains(strings.Join(env, "\n"), "RESTIC_PASSWORD=test-secret") {
		t.Fatal(er)
	}
	r.D.PasswordEnv = "LXCER_MISSING_VARIABLE"
	if _, er = r.env(); er == nil {
		t.Fatal("missing secret accepted")
	}
	file := filepath.Join(t.TempDir(), "password")
	if er = os.WriteFile(file, []byte("secret"), 0644); er != nil {
		t.Fatal(er)
	}
	c := Config{Version: 1, Workers: 1, Timeout: time.Minute, Compression: "none", Destinations: []Destination{{Name: "one", Path: "r", PasswordFile: file}}}
	if er = c.Validate(); er == nil {
		t.Fatal("unsafe permissions accepted")
	}
	if er = os.Chmod(file, 0600); er != nil {
		t.Fatal(er)
	}
	if er = c.Validate(); er != nil {
		t.Fatal(er)
	}
}
func TestRetryCancellationAndMissingSummary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r := Repository{D: Destination{Name: "one", Path: "r"}, Retries: 2, R: runnerFunc(func(context.Context, []string, []byte, string, ...string) ([]byte, error) {
		return nil, errors.New("repository locked")
	})}
	if _, er := r.run(ctx, "backup"); er == nil {
		t.Fatal("retry failure accepted")
	}
	r.Retries = 0
	r.R = runnerFunc(func(context.Context, []string, []byte, string, ...string) ([]byte, error) {
		return []byte(`{"message_type":"summary"}`), nil
	})
	if _, er := r.backup(context.Background(), []string{"/file"}, nil); er == nil {
		t.Fatal("missing snapshot ID accepted")
	}
}
func TestConfigOverridesAndPolicyPrecedence(t *testing.T) {
	base := Policy{Strategy: "auto", AllowExcluded: true, Hooks: Hooks{Pre: []Hook{{Argv: []string{"old"}}}}}
	p := mergePolicy(base, Policy{Strategy: "snapshot", Paths: []string{"/tmp/x"}, Hooks: Hooks{Pre: []Hook{}}})
	if p.Strategy != "snapshot" || !p.AllowExcluded || len(p.Hooks.Pre) != 0 || len(p.Paths) != 1 {
		t.Fatal(p)
	}
	path := filepath.Join(t.TempDir(), "conf.yml")
	if er := os.WriteFile(path, []byte("destinations:\n - name: one\n   path: /tmp/repo\n   password_env: IGNORED_PASSWORD_ENV\n"), 0600); er != nil {
		t.Fatal(er)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if er := os.WriteFile(secret, []byte("password"), 0600); er != nil {
		t.Fatal(er)
	}
	t.Setenv("LXCER_STATE_DIR", t.TempDir())
	t.Setenv("LXCER_PASSWORD_FILE", secret)
	c, er := readConfig(path)
	if er != nil || c.Destinations[0].Password != "" || c.Destinations[0].PasswordFile != secret {
		t.Fatal(er)
	}
}
func TestCommandHandlersWithoutLXD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf.yml")
	if er := os.WriteFile(path, []byte("state_dir: "+filepath.Join(dir, "state")+"\n"), 0600); er != nil {
		t.Fatal(er)
	}
	for _, tc := range []struct {
		args    []string
		success bool
	}{{[]string{"validate"}, true}, {[]string{"list"}, true}, {[]string{"cleanup"}, true}, {[]string{"recover"}, true}, {[]string{"backup"}, false}, {[]string{"capabilities"}, false}, {[]string{"verify"}, false}, {[]string{"restore", "-local"}, false}, {[]string{"discover", "-destination", "missing"}, false}, {[]string{"retention", "-destination", "missing"}, false}, {[]string{"unknown"}, false}} {
		var b bytes.Buffer
		args := append(append([]string{}, tc.args...), "-config", path)
		code := run(args, &b, &b)
		if (code == 0) != tc.success {
			t.Fatalf("args=%v code=%d output=%s", args, code, b.String())
		}
	}
}

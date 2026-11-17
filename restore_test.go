package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backedUp(t *testing.T) (Engine, *fakeRunner, Manifest) {
	t.Helper()
	f := fake()
	e := testEngine(t, f)
	if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er != nil {
		t.Fatal(er)
	}
	return e, f, manifests(t, e)[0]
}
func TestRestoreFailureIsolationAndOwnership(t *testing.T) {
	for _, failure := range []string{"restic restore", "lxc import", "lxc config edit", "lxc exec lab:restored -- unhealthy", "checksum", "collision", "unsafe-target", "network-profile"} {
		t.Run(failure, func(t *testing.T) {
			e, f, m := backedUp(t)
			o := RestoreOptions{Name: "restored", Host: "lab", Destination: "primary", Test: true, Isolated: true, Profile: "isolated"}
			switch failure {
			case "checksum":
				m.SHA256 = strings.Repeat("0", 64)
			case "collision":
				f.instances["lab"] = []Container{instance("restored", 102)}
			case "unsafe-target":
				o.Host = "local"
			case "network-profile":
				f.fail = "/1.0/profiles/"
			default:
				f.fail = failure
			}
			if strings.Contains(failure, "unhealthy") {
				o.Health = []string{"unhealthy"}
			}
			if er := e.restore(context.Background(), &m, o); er == nil {
				t.Fatal("restore failure reported success")
			}
			if m.RestoreTest == nil || m.RestoreTest.Success {
				t.Fatal("failure result not recorded")
			}
			if failure == "collision" && len(f.instances["lab"]) != 1 {
				t.Fatal("collision resource deleted")
			}
			if failure == "lxc config edit" && len(f.instances["lab"]) != 1 {
				t.Fatal("unowned imported instance deleted")
			}
		})
	}
}
func TestRestorePlanDoesNotMutate(t *testing.T) {
	e, f, m := backedUp(t)
	before := len(f.calls)
	if er := e.restore(context.Background(), &m, RestoreOptions{Name: "planned", Host: "lab", Destination: "primary", DryRun: true}); er != nil {
		t.Fatal(er)
	}
	for _, call := range f.calls[before:] {
		if strings.HasPrefix(call, "restic restore") || strings.HasPrefix(call, "lxc import ") {
			t.Fatal("dry run mutated resources")
		}
	}
}
func TestSuccessfulRestoreStoppedAndConfig(t *testing.T) {
	e, f, m := backedUp(t)
	m.Instance.ExpandedConfig = map[string]string{"limits.cpu": "3", "boot.autostart": "true"}
	m.Instance.Config["boot.autostart"] = "true"
	m.Instance.ExpandedDevices["net"] = map[string]string{"type": "nic", "network": "prod"}
	o := RestoreOptions{Name: "restored", Host: "lab", Destination: "primary", Networks: map[string]string{"prod": "dr"}, Pool: "other"}
	if er := e.restore(context.Background(), &m, o); er != nil {
		t.Fatal(er)
	}
	c := f.instances["lab"][0]
	if c.Config["limits.cpu"] != "3" {
		t.Fatal("inherited profile configuration lost")
	}
	if c.StatusCode != 102 || c.Config["boot.autostart"] != "false" || c.ExpandedDevices["net"]["network"] != "dr" || c.ExpandedDevices["root"]["pool"] != "other" {
		t.Fatalf("incorrect restored config %+v", c)
	}
}
func TestManagedVolumeAndBindMount(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	e.C.Policy.BackupVolumes = true
	f.instances["local"][1].ExpandedDevices["data"] = map[string]string{"type": "disk", "pool": "default", "source": "data", "path": "/data"}
	if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er != nil {
		t.Fatal(er)
	}
	m := manifests(t, e)[0]
	if len(m.Volumes) != 1 {
		t.Fatal("managed volume not exported")
	}
	o := RestoreOptions{Name: "restore", Host: "lab", Destination: "primary", RestoreVolumes: true}
	if er := e.restore(context.Background(), &m, o); er != nil {
		t.Fatal(er)
	}
	if f.instances["lab"][0].ExpandedDevices["data"]["source"] != "restore-data" {
		t.Fatal("volume not remapped")
	}
	f = fake()
	e = testEngine(t, f)
	path := t.TempDir()
	if er := os.WriteFile(filepath.Join(path, "external"), []byte("data"), 0600); er != nil {
		t.Fatal(er)
	}
	f.instances["local"][1].ExpandedDevices["bind"] = map[string]string{"type": "disk", "source": path, "path": "/data"}
	e.C.Policy.Paths = []string{path}
	if er := e.Backup(context.Background(), []Host{{Name: "local"}}, "stopped"); er != nil {
		t.Fatal(er)
	}
	m = manifests(t, e)[0]
	for _, a := range m.Attachments {
		if !a.Included {
			t.Fatal("configured bind path not included")
		}
	}
	if _, er := os.Stat(filepath.Join(path, "external")); er != nil {
		t.Fatal("source bind path deleted")
	}
}
func TestLegacyAdoption(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	archive := filepath.Join(t.TempDir(), "old.tar.zst")
	if er := os.WriteFile(archive, []byte("legacy bytes"), 0600); er != nil {
		t.Fatal(er)
	}
	r := e.repo(e.C.Destinations[0])
	id, er := r.backup(context.Background(), []string{archive}, nil)
	if er != nil {
		t.Fatal(er)
	}
	metadata := filepath.Join(t.TempDir(), "metadata.json")
	b, _ := json.Marshal(instance("legacy", 102))
	if er = os.WriteFile(metadata, b, 0600); er != nil {
		t.Fatal(er)
	}
	m, er := e.adoptLegacy(context.Background(), "primary", id, archive, metadata, "old")
	if er != nil {
		t.Fatal(er)
	}
	if m.Started.Unix() != 1600000000 {
		t.Fatal("adoption reset original backup age")
	}
	if !m.Legacy || m.SHA256 == "" {
		t.Fatal("bad adoption")
	}
	if _, er = loadManifest(filepath.Join(e.C.StateDir, m.ID, "manifest.json")); er != nil {
		t.Fatal(er)
	}
}
func TestCrashRecoveryResumesEvenTerminalFailure(t *testing.T) {
	f := fake()
	e := testEngine(t, f)
	id := newID()
	dir := filepath.Join(e.C.StateDir, id)
	if er := privateDir(dir); er != nil {
		t.Fatal(er)
	}
	m := Manifest{Version: 1, ID: id, Host: "local", Instance: instance("stopped", 102), State: Failed, NeedsResume: true, ResumeHooks: []Hook{{Argv: []string{"resume"}}}, Completed: time.Now()}
	if er := m.save(dir); er != nil {
		t.Fatal(er)
	}
	if er := e.recoverJobs(context.Background(), true); er != nil {
		t.Fatal(er)
	}
	if manifests(t, e)[0].NeedsResume {
		t.Fatal("resume intent still set")
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "-- resume") {
		t.Fatal("resume not invoked")
	}
}
func TestCatalogSelectionAndRestoreList(t *testing.T) {
	e, _, m := backedUp(t)
	selected, _, er := e.findManifest(m.Instance.Name, "local")
	if er != nil || selected.ID != m.ID {
		t.Fatal(er)
	}
	path := filepath.Join(t.TempDir(), "restore.list")
	if er = os.WriteFile(path, []byte("stopped:copy\nrunning:copy2\n"), 0600); er != nil {
		t.Fatal(er)
	}
	pairs, er := loadRestoreList(path)
	if er != nil || len(pairs) != 2 {
		t.Fatal(er)
	}
	if er = os.WriteFile(path, []byte("bad-line\n"), 0600); er != nil {
		t.Fatal(er)
	}
	if _, er = loadRestoreList(path); er == nil {
		t.Fatal("malformed line accepted")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RestoreOptions struct {
	RestoreVolumes bool
	Name           string
	Host           string
	Destination    string
	Snapshot       string
	Pool           string
	Networks       map[string]string
	DryRun         bool
	Test           bool
	Isolated       bool
	Profile        string
	Health         []string
	NativeTarget   string
}

func (e Engine) destination(name string) (Destination, error) {
	for _, d := range e.C.Destinations {
		if d.Name == name {
			return d, nil
		}
	}
	return Destination{}, errors.New("unknown destination")
}
func (e Engine) restore(ctx context.Context, m *Manifest, opt RestoreOptions) (result error) {
	if !validName(opt.Name) || !validName(opt.Host) {
		return errors.New("valid restore name and host required")
	}

	start := time.Now()
	if opt.Test {
		defer func() {
			rt := &RestoreTest{Time: time.Now().UTC(), Duration: time.Since(start).Seconds(), BackupAge: time.Since(m.Started).Seconds(), Success: result == nil}
			if result != nil {
				rt.Error = result.Error()
			}
			m.RestoreTest = rt
		}()
	}
	d, err := e.destination(opt.Destination)
	if err != nil {
		return err
	}
	r := e.repo(d)
	id := opt.Snapshot
	if id == "" {
		for _, s := range m.Destinations {
			if s.Name == d.Name && s.Verified {
				id = s.SnapshotID
			}
		}
	}
	if !snapshotID(id) {
		return errors.New("select a verified data snapshot ID")
	}
	if m.Strategy == "native" {
		if opt.Test && len(opt.Health) > 0 {
			return errors.New("native offline restore tests cannot execute instance health commands")
		}
		if opt.NativeTarget == "" {
			return errors.New("native backup requires -native-target (a new directory); instance reconstruction uses manifest metadata")
		}
		if opt.DryRun {
			return nil
		}
		if err = r.verify(ctx, id, m.Paths); err != nil {
			return err
		}
		if _, err = os.Lstat(opt.NativeTarget); !os.IsNotExist(err) {
			return errors.New("native restore target must not exist")
		}
		if err = os.MkdirAll(opt.NativeTarget, 0700); err != nil {
			return err
		}
		_, err = r.run(ctx, "restore", id, "--target", opt.NativeTarget, "--verify")
		if err != nil {
			return err
		}
		for _, path := range m.Paths {
			if _, err = os.Lstat(filepath.Join(opt.NativeTarget, strings.TrimPrefix(filepath.Clean(path), "/"))); err != nil {
				return err
			}
		}
		return nil
	}
	l := LXD{R: e.R, Host: opt.Host}
	if err = l.Detect(ctx); err != nil {
		return err
	}
	cc, err := l.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range cc {
		if c.Name == opt.Name {
			return errors.New("restore collision: choose another name; overwrite is intentionally unavailable")
		}
	}
	if m.Strategy != "snapshot" && opt.Name != m.Instance.Name && !l.Caps.Has("backup_override_name") {
		return errors.New("target cannot override backup instance name; use legacy image strategy or a capable target")
	}
	if opt.Pool != "" && !validName(opt.Pool) {
		return errors.New("invalid pool")
	}
	if opt.Pool != "" && m.Strategy != "snapshot" && !l.Caps.Has("container_backup_override_pool") {
		return errors.New("target cannot map backup storage pool")
	}
	if opt.Test {
		if !opt.Isolated || opt.Host == m.Host || opt.Host == "local" || !validName(opt.Profile) {
			return errors.New("restore-test requires an explicitly isolated, separate remote host and root-only profile")
		}
		b, er := l.run(ctx, "query", l.ref("/1.0/profiles/"+opt.Profile))
		if er != nil {
			return er
		}
		var profile struct {
			Devices map[string]map[string]string `json:"devices"`
			Config  map[string]string            `json:"config"`
		}
		if er = json.Unmarshal(b, &profile); er != nil {
			return er
		}
		if len(profile.Devices) == 0 {
			return errors.New("isolation profile must contain a root disk")
		}
		for _, v := range profile.Devices {
			if v["type"] != "disk" || v["path"] != "/" {
				return errors.New("isolation profile must contain only root disks")
			}
		}
		for k := range profile.Config {
			if strings.HasPrefix(k, "raw.") || strings.HasPrefix(k, "security.") {
				return errors.New("isolation profile contains unsafe configuration")
			}
		}
	}
	for _, a := range m.Attachments {
		if a.Target != "/" && !opt.Test {
			if a.Handling == "separate managed volume export" {
				if !opt.RestoreVolumes {
					return errors.New("managed volumes require -restore-volumes or restore them separately")
				}
			} else {
				return errors.New("external disks require separate filesystem restoration; use a root-only isolated restore test until dependencies are remapped")
			}
		}
	}
	if opt.DryRun {
		return nil
	}
	if err = privateDir(e.C.StateDir); err != nil {
		return err
	}
	lock, err := acquireLock(e.C.StateDir, opt.Host+"/"+opt.Name)
	if err != nil {
		return err
	}
	defer releaseLock(lock)
	owned := false
	created := false
	staging, err := os.MkdirTemp(e.C.StateDir, "restore-")
	if err != nil {
		return err
	}
	defer func() {
		if result == nil {
			result = os.RemoveAll(staging)
		}
	}()
	// Require exact archive containment; Restic restores the original absolute path beneath staging.
	if m.Artifact == "" || !filepath.IsAbs(m.Artifact) {
		return errors.New("manifest has no absolute artifact path")
	}
	if err = r.verify(ctx, id, []string{m.Artifact}); err != nil {
		return err
	}
	if _, err = r.run(ctx, "restore", id, "--target", staging, "--include", m.Artifact, "--verify"); err != nil {
		return err
	}
	artifact := filepath.Join(staging, strings.TrimPrefix(filepath.Clean(m.Artifact), "/"))
	sum, _, err := checksum(artifact)
	if err != nil {
		return err
	}
	if sum != m.SHA256 {
		return errors.New("restore archive checksum mismatch")
	}
	if strings.HasSuffix(artifact, ".zst") {
		if _, err = e.R.Run(ctx, nil, nil, "zstd", "-d", "-T1", artifact, "-o", artifact+".tar"); err != nil {
			return err
		}
		artifact += ".tar"
	}

	volumeNames := map[string]string{}
	if opt.RestoreVolumes && !opt.Test {
		for _, v := range m.Volumes {
			newName := opt.Name + "-" + v.Name
			if !validName(newName) {
				return errors.New("restored volume name too long")
			}
			targetPool := v.Pool
			if opt.Pool != "" {
				targetPool = opt.Pool
			}
			b, er := l.run(ctx, "storage", "volume", "list", l.ref(targetPool), "--format", "json")
			if er != nil {
				return er
			}
			var existing []struct {
				Name string `json:"name"`
			}
			if er = json.Unmarshal(b, &existing); er != nil {
				return er
			}
			for _, x := range existing {
				if x.Name == newName {
					return errors.New("managed volume restore collision")
				}
			}
			if _, er = r.run(ctx, "restore", id, "--target", staging, "--include", v.Artifact, "--verify"); er != nil {
				return er
			}
			vp := filepath.Join(staging, strings.TrimPrefix(filepath.Clean(v.Artifact), "/"))
			sum, _, er := checksum(vp)
			if er != nil {
				return er
			}
			if sum != v.SHA256 {
				return errors.New("volume checksum mismatch")
			}
			if _, er = l.run(ctx, "storage", "volume", "import", l.ref(targetPool), vp, newName); er != nil {
				return er
			}
			volumeNames[v.Device] = newName
		}
	}
	alias := newID()
	journal, _ := json.Marshal(map[string]any{"version": 1, "id": alias, "source_job": m.ID, "target_host": opt.Host, "target_instance": opt.Name, "isolated_test": opt.Test, "status": "import pending"})
	if err = atomicWrite(filepath.Join(staging, "restore.json"), journal); err != nil {
		return err
	}
	defer func() {
		if opt.Test && created && owned {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, er := l.run(cleanupCtx, "delete", l.ref(opt.Name), "--force")
			result = errors.Join(result, er)
		}
	}()
	if m.Strategy == "snapshot" {
		if _, err = l.run(ctx, "image", "import", artifact, l.ref(""), "--alias", alias); err != nil {
			return err
		}
		a := []string{"init", l.ref(alias), l.ref(opt.Name), "-c", "boot.autostart=false", "-c", "user.lxcer.restore=" + alias}
		if opt.Pool != "" {
			a = append(a, "-s", opt.Pool)
		}
		if opt.Test {
			a = append(a, "-p", opt.Profile)
		}
		if _, err = l.run(ctx, a...); err != nil {
			return err
		}
		created = true
		owned = true
		if _, err = l.run(ctx, "image", "alias", "delete", l.ref(alias)); err != nil {
			return err
		}
	} else {
		a := []string{"import", l.ref(""), artifact}
		if l.Caps.Has("backup_override_name") {
			a = append(a, opt.Name)
		}
		if opt.Pool != "" {
			a = append(a, "--storage", opt.Pool)
		}
		if _, err = l.run(ctx, a...); err != nil {
			return err
		}
		created = true
	}
	// Construct configuration explicitly; do not inherit source host profiles or autostart.
	cfg := map[string]string{}
	for k, v := range m.Instance.Config {
		if !strings.HasPrefix(k, "volatile.") && !strings.HasPrefix(k, "raw.") {
			cfg[k] = v
		}
	}
	cfg["boot.autostart"] = "false"
	cfg["user.lxcer.restore"] = alias
	devices := map[string]map[string]string{}
	sourceDevices := m.Instance.ExpandedDevices
	if sourceDevices == nil {
		sourceDevices = m.Instance.Devices
	}
	for n, d := range sourceDevices {
		copy := map[string]string{}
		for k, v := range d {
			copy[k] = v
		}
		if opt.Test {
			if d["type"] != "disk" || d["path"] != "/" {
				devices[n] = map[string]string{"type": "none"}
				continue
			}
		}
		if d["type"] == "disk" && d["path"] == "/" && opt.Pool != "" {
			copy["pool"] = opt.Pool
		}
		if d["type"] == "nic" {
			for _, key := range []string{"network", "parent"} {
				if v, ok := opt.Networks[d[key]]; ok {
					copy[key] = v
				}
			}
		}
		if vn, ok := volumeNames[n]; ok {
			copy["source"] = vn
			if opt.Pool != "" {
				copy["pool"] = opt.Pool
			}
		}
		devices[n] = copy
	}
	profiles := []string{}
	if opt.Test {
		profiles = []string{opt.Profile}
		for k := range cfg {
			if strings.HasPrefix(k, "security.") {
				delete(cfg, k)
			}
		}
	}
	document := map[string]any{"architecture": m.Instance.Architecture, "config": cfg, "devices": devices, "profiles": profiles, "ephemeral": false}
	b, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if _, err = e.R.Run(ctx, nil, b, "lxc", "config", "edit", l.ref(opt.Name)); err != nil {
		return err
	}
	owned = true
	cc, err = l.List(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, c := range cc {
		if c.Name == opt.Name {
			found = true
			if c.StatusCode != 102 {
				return errors.New("restored instance unexpectedly running")
			}
			if c.Config["user.lxcer.restore"] != alias {
				return errors.New("restored instance ownership validation failed")
			}
			if opt.Test {
				for _, d := range c.ExpandedDevices {
					if d["type"] != "none" && (d["type"] != "disk" || d["path"] != "/") {
						return errors.New("restore test isolation validation failed")
					}
				}
			}
		}
	}
	if !found {
		return errors.New("restored instance missing after import")
	}
	if opt.Test && len(opt.Health) > 0 {
		if _, err = l.run(ctx, "start", l.ref(opt.Name)); err != nil {
			return err
		}
		if _, err = l.run(ctx, append([]string{"exec", l.ref(opt.Name), "--"}, opt.Health...)...); err != nil {
			return err
		}
	}
	return nil
}
func (e Engine) recoverCatalog(ctx context.Context, d Destination, id, target string) error {
	if !snapshotID(id) {
		return errors.New("exact receipt snapshot ID required")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return errors.New("catalog recovery target must not exist")
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	_, err := e.repo(d).run(ctx, "restore", id, "--target", target, "--verify")
	return err
}
func (e Engine) recoverJobs(ctx context.Context, execute bool) error {
	files, err := filepath.Glob(filepath.Join(e.C.StateDir, "lxcer-*", "manifest.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		m, er := loadManifest(f)
		if er != nil {
			return er
		}
		if m.Completed.IsZero() || m.NeedsResume {
			lock, er := acquireLock(e.C.StateDir, m.Host+"/"+m.Instance.Name)
			if er != nil {
				continue
			}
			if execute {
				if m.NeedsResume {
					l := LXD{R: e.R, Host: m.Host}
					if er = e.hooks(ctx, l, m.Instance, m.ResumeHooks); er != nil {
						releaseLock(lock)
						return er
					}
					m.NeedsResume = false
				}
				m.State = Failed
				m.Error = "interrupted job: retained resources; verify repositories and resume application before manual recovery"
				m.Completed = time.Now().UTC()
				er = m.save(filepath.Dir(f))
			}
			releaseLock(lock)
			if er != nil {
				return er
			}
			fmt.Printf("%s interrupted; resources retained\n", m.ID)
		}
		if m.State == Verified {
			fmt.Printf("%s eligible for cleanup\n", m.ID)
			if execute {
				if er = e.cleanup(ctx, &m, filepath.Dir(f), false); er != nil {
					return er
				}
			}
		}
	}
	return nil
}

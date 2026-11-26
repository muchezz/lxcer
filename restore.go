package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RestoreOptions struct {
	ReceiveDataset string
	Start          bool
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
	if name == "" && len(e.C.Destinations) == 1 {
		return e.C.Destinations[0], nil
	}
	if name == "legacy-restore" && e.C.RestoreResticRepo.Path != "" {
		d := e.C.RestoreResticRepo
		d.Name = name
		return d, nil
	}
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
	imageRebuild := m.Strategy == "native" || m.Strategy == "storage-incremental"
	if opt.NativeTarget != "" {
		if m.Strategy != "native" {
			return errors.New("-native-target requires a native filesystem backup")
		}
		if opt.Test && len(opt.Health) > 0 {
			return errors.New("native offline restore tests cannot execute instance health commands")
		}
		if opt.DryRun {
			return r.verify(ctx, id, m.Paths)
		}
		if err = r.verify(ctx, id, m.Paths); err != nil {
			return err
		}
		if _, err = os.Lstat(opt.NativeTarget); !os.IsNotExist(err) {
			return errors.New("native restore target must not exist")
		}
		if err = privateDir(e.C.StateDir); err != nil {
			return err
		}
		if err = makeRestoreTarget(opt.NativeTarget); err != nil {
			return err
		}
		offlineCtx, guard, stop := e.guard(ctx, opt.NativeTarget)
		defer stop()
		defer func() { result = errors.Join(result, context.Cause(offlineCtx)) }()
		if err = guard.check(); err != nil {
			return err
		}
		_, err = r.run(offlineCtx, "restore", id, "--target", opt.NativeTarget, "--verify")
		if err != nil {
			return err
		}
		if err = guard.check(); err != nil {
			return err
		}
		for _, path := range m.Paths {
			if _, err = os.Lstat(filepath.Join(opt.NativeTarget, strings.TrimPrefix(filepath.Clean(path), "/"))); err != nil {
				return err
			}
		}
		return nil
	}
	l := LXD{Host: opt.Host}
	if err = l.Detect(ctx); err != nil {
		return err
	}
	renameImport := !imageRebuild && m.Strategy != "snapshot" && opt.Name != m.Instance.Name && (!l.Caps.Has("backup_override_name") || !l.Caps.ImportName)
	if !opt.DryRun {
		if err = privateDir(e.C.StateDir); err != nil {
			return err
		}
		lock, err := acquireLock(e.C.StateDir, opt.Host+"/"+opt.Name)
		if err != nil {
			return err
		}
		defer releaseLock(lock)
		if renameImport {
			originalLock, er := acquireLock(e.C.StateDir, opt.Host+"/"+m.Instance.Name)
			if er != nil {
				return er
			}
			defer releaseLock(originalLock)
		}
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
	if renameImport {
		for _, c := range cc {
			if c.Name == m.Instance.Name {
				return errors.New("legacy full import needs the original name free before rename; use a separate compatible host or an image backup")
			}
		}
	}
	if opt.Pool != "" && !validName(opt.Pool) {
		return errors.New("invalid pool")
	}
	if opt.Pool != "" && !imageRebuild && m.Strategy != "snapshot" && !l.Caps.Has("container_backup_override_pool") {
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
		b, er = unwrapAPI(b)
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
	if imageRebuild {
		if m.Instance.Type == "virtual-machine" || m.Instance.Architecture == "" {
			return errors.New("native reconstruction requires container architecture")
		}
		if m.Stream != nil {
			if err = validateStream(*m); err != nil {
				return err
			}
			if m.Stream.Driver == "zfs" && !datasetPattern.MatchString(opt.ReceiveDataset) {
				return errors.New("ZFS reconstruction requires a disposable -receive-dataset")
			}
		}
	}
	if opt.DryRun {
		return r.verify(ctx, id, m.Paths)
	}
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
	alias := newID()
	journal, _ := json.Marshal(map[string]any{"version": 1, "id": alias, "source_job": m.ID, "target_host": opt.Host, "target_instance": opt.Name, "isolated_test": opt.Test, "status": "import pending", "original_name": m.Instance.Name, "rename_after_import": renameImport, "receive_parent": opt.ReceiveDataset, "receive_directory": filepath.Join(staging, "received")})
	if err = atomicWrite(filepath.Join(staging, "restore.json"), journal); err != nil {
		return err
	}

	ctx, g, stopGuard := e.guard(ctx, staging)
	defer stopGuard()
	defer func() { result = errors.Join(result, context.Cause(ctx)) }()
	if err = g.check(); err != nil {
		return err
	}
	var artifact string
	if imageRebuild {
		root, release, er := e.reconstructNative(ctx, r, id, staging, *m, opt)
		defer func() {
			if result == nil {
				result = errors.Join(result, release())
			}
		}()
		if er != nil {
			return er
		}
		artifact, err = e.nativeImage(ctx, root, staging, *m)
		if err != nil {
			return err
		}
	} else {
		// Require exact archive containment; Restic restores the original absolute path beneath staging.
		if m.Artifact == "" || !filepath.IsAbs(m.Artifact) {
			return errors.New("manifest has no absolute artifact path")
		}
		if err = r.verify(ctx, id, []string{m.Artifact}); err != nil {
			return err
		}
		if err = r.restorePath(ctx, id, staging, m.Artifact); err != nil {
			return err
		}
		artifact = filepath.Join(staging, strings.TrimPrefix(filepath.Clean(m.Artifact), "/"))
		sum, _, err := checksum(ctx, artifact)
		if err != nil {
			return err
		}
		if sum != m.SHA256 {
			return errors.New("restore archive checksum mismatch")
		}
		if strings.HasSuffix(artifact, ".zst") {
			if _, err = runCommand(ctx, "zstd", "-d", "-T1", artifact, "-o", artifact+".tar"); err != nil {
				return err
			}
			artifact += ".tar"
		}

	}
	if err = g.check(); err != nil {
		return err
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
			if er = r.restorePath(ctx, id, staging, v.Artifact); er != nil {
				return er
			}
			vp := filepath.Join(staging, strings.TrimPrefix(filepath.Clean(v.Artifact), "/"))
			sum, _, er := checksum(ctx, vp)
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
	defer func() {
		if opt.Test && created && owned {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			marker, err := l.run(cleanupCtx, "config", "get", l.ref(opt.Name), "user.lxcer.restore")
			if err == nil {
				if strings.TrimSpace(string(marker)) == alias {
					_, err = l.run(cleanupCtx, "delete", l.ref(opt.Name), "--force")
				} else {
					err = errors.New("restore target ownership changed")
				}
			}
			if err != nil {
				result = errors.Join(result, fmt.Errorf("restore test cleanup: %w", err))
			}
		}
	}()
	if m.Strategy == "snapshot" || imageRebuild {
		importArgs := []string{"image", "import", artifact}
		for _, part := range m.ImageParts {
			if !filepath.IsAbs(part.Artifact) || filepath.Base(filepath.Dir(part.Artifact)) != m.ID {
				return errors.New("split image ownership mismatch")
			}
			if err = r.verify(ctx, id, []string{part.Artifact}); err != nil {
				return err
			}
			if err = r.restorePath(ctx, id, staging, part.Artifact); err != nil {
				return err
			}
			path := filepath.Join(staging, strings.TrimPrefix(filepath.Clean(part.Artifact), "/"))
			sum, _, er := checksum(ctx, path)
			if er != nil {
				return er
			}
			if sum != part.SHA256 {
				return errors.New("split image checksum mismatch")
			}
			importArgs = append(importArgs, path)
		}
		importArgs = append(importArgs, l.ref(""), "--alias", alias)
		if err = g.check(); err != nil {
			return err
		}
		if _, err = l.run(ctx, importArgs...); err != nil {
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
		if l.Caps.Has("backup_override_name") && l.Caps.ImportName {
			a = append(a, opt.Name)
		}
		if opt.Pool != "" {
			a = append(a, "--storage", opt.Pool)
		}
		if _, err = l.run(ctx, a...); err != nil {
			return err
		}
		created = true
		if renameImport {
			if _, err = l.run(ctx, "move", l.ref(m.Instance.Name), l.ref(opt.Name)); err != nil {
				return err
			}
		}
	}
	// Construct configuration explicitly; do not inherit source host profiles or autostart.
	cfg := map[string]string{}
	for k, v := range effectiveConfig(m.Instance) {
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
	if _, err = runOutput(ctx, nil, b, "lxc", "config", "edit", l.ref(opt.Name)); err != nil {
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
	if opt.Start && !opt.Test {
		if _, err = l.run(ctx, "start", l.ref(opt.Name)); err != nil {
			return err
		}
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
func (e Engine) saveRestoreTest(path string, result Manifest) error {
	if err := privateDir(e.C.StateDir); err != nil {
		return err
	}
	lock, err := acquireLock(e.C.StateDir, sourceRemote(result)+"/"+result.Instance.Name)
	if err != nil {
		return fmt.Errorf("record restore test result: %w", err)
	}
	defer releaseLock(lock)
	current, err := loadManifest(path)
	if err != nil {
		return err
	}
	if current.ID != result.ID || current.Host != result.Host || current.Instance.Name != result.Instance.Name {
		return errors.New("manifest identity changed during restore test")
	}
	if current.RestoreTest != nil && !result.RestoreTest.Time.After(current.RestoreTest.Time) {
		return nil
	}
	current.RestoreTest = result.RestoreTest
	return current.save(filepath.Dir(path))
}

func (e Engine) recoverCatalog(ctx context.Context, d Destination, id, target string) error {
	r := e.repo(d)
	snapshot, err := r.snapshot(ctx, id)
	if err != nil {
		return err
	}
	var manifestPath string
	for _, path := range snapshot.Paths {
		parent := filepath.Base(filepath.Dir(path))
		if filepath.IsAbs(path) && validName(parent) && strings.HasPrefix(parent, "lxcer-") && filepath.Base(path) == "manifest.json" {
			if manifestPath != "" {
				return errors.New("snapshot contains multiple catalog manifest paths")
			}
			manifestPath = path
		}
	}
	if manifestPath == "" {
		return errors.New("snapshot does not contain an lxcer catalog manifest")
	}
	if err := r.verifyNode(ctx, snapshot.ID, manifestPath); err != nil {
		return err
	}
	if err := privateDir(e.C.StateDir); err != nil {
		return err
	}
	if err := makeRestoreTarget(target); err != nil {
		return err
	}
	ctx, guard, stop := e.guard(ctx, target)
	defer stop()
	if err := guard.check(); err != nil {
		return err
	}
	if err := r.restorePath(ctx, snapshot.ID, target, manifestPath); err != nil {
		return err
	}
	if err := guard.check(); err != nil {
		return err
	}
	_, err = loadManifest(filepath.Join(target, strings.TrimPrefix(filepath.Clean(manifestPath), "/")))
	return errors.Join(err, context.Cause(ctx))
}

func makeRestoreTarget(target string) error {
	if target == "" {
		return errors.New("new restore target directory required")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	return os.Mkdir(target, 0700)
}

func (e Engine) recoverJobs(ctx context.Context, out io.Writer, execute bool) error {
	files, err := manifestPaths(e.C.StateDir)
	if err != nil {
		return err
	}
	if execute && len(files) > 0 {
		if err := privateDir(e.C.StateDir); err != nil {
			return err
		}
	}
	var errs []error
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := e.recoverJob(ctx, out, path, execute); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func (e Engine) recoverJob(ctx context.Context, out io.Writer, path string, execute bool) error {
	m, err := loadManifest(path)
	if err != nil {
		return err
	}
	if m.Legacy || (m.Cleanup == "completed" && !m.NeedsResume && !m.Completed.IsZero()) {
		return nil
	}
	if !execute {
		return e.recoverManifest(ctx, out, path, m, false)
	}
	lock, err := acquireLock(e.C.StateDir, sourceRemote(m)+"/"+m.Instance.Name)
	if errors.Is(err, errJobLocked) {
		return nil
	}
	if err != nil {
		return err
	}
	defer releaseLock(lock)

	// An active backup could have completed between the initial read and the lock.
	current, err := loadManifest(path)
	if err != nil {
		return err
	}
	if current.ID != m.ID || current.Host != m.Host || sourceRemote(current) != sourceRemote(m) || current.Instance.Name != m.Instance.Name {
		return errors.New("job identity changed while acquiring recovery lock")
	}
	return e.recoverManifest(ctx, out, path, current, true)
}

func (e Engine) recoverManifest(ctx context.Context, out io.Writer, path string, m Manifest, execute bool) error {
	if m.Legacy || (m.Cleanup == "completed" && !m.NeedsResume && !m.Completed.IsZero()) {
		return nil
	}
	dir := filepath.Dir(path)
	if m.NeedsResume || (m.Completed.IsZero() && (m.State != Verified || !m.verificationComplete())) {
		if !execute {
			fmt.Fprintf(out, "%s unfinished; resources retained\n", m.ID)
			return nil
		}
		fmt.Fprintf(out, "%s interrupted; resources retained\n", m.ID)
		if m.NeedsResume {
			l := LXD{Host: sourceRemote(m)}
			if err := e.hooks(ctx, l, m.Instance, m.ResumeHooks); err != nil {
				return err
			}
			m.NeedsResume = false
		}
		m.State = Failed
		m.Error = "interrupted job; resources retained"
		m.Completed = time.Now().UTC()
		return m.save(dir)
	}
	if m.State != Verified {
		fmt.Fprintf(out, "%s retained state=%s snapshot=%s image=%s artifact=%s\n", m.ID, m.State, m.Snapshot, m.ImageFingerprint, m.Artifact)
		return nil
	}
	if !m.verificationComplete() {
		return errors.New("required destination verification is incomplete; resources retained")
	}
	for _, s := range m.Destinations {
		if !s.Verified || !snapshotID(s.SnapshotID) || !snapshotID(s.ReceiptID) {
			fmt.Fprintf(out, "%s missing verified destination receipt; resources retained\n", m.ID)
			return nil
		}
	}
	fmt.Fprintf(out, "%s eligible for cleanup\n", m.ID)
	if !execute {
		return nil
	}
	if m.Completed.IsZero() {
		m.Completed = time.Now().UTC()
		m.Error = "backup command interrupted after verification"
		if err := m.save(dir); err != nil {
			return err
		}
	}
	if m.Cleanup == "completed" {
		return nil
	}
	for _, s := range m.Destinations {
		d, err := e.destination(s.Name)
		if err != nil {
			return err
		}
		r := e.repo(d)
		if err := r.verify(ctx, s.SnapshotID, m.Paths); err != nil {
			return err
		}
		if err := r.verify(ctx, s.ReceiptID, nil); err != nil {
			return err
		}
	}
	return e.cleanup(ctx, &m, dir)
}

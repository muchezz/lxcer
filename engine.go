package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	C   Config
	Log *slog.Logger
}

func (e Engine) repo(d Destination) Repository {
	return Repository{D: d, Retries: e.C.Retries, Bandwidth: e.C.Bandwidth, StateDir: e.C.StateDir}
}
func (e Engine) hooks(ctx context.Context, l LXD, c Container, hs []Hook) error {
	var errs []error
	for _, h := range hs {
		timeout := h.Timeout
		if timeout == 0 {
			timeout = 2 * time.Minute
		}
		hc, cancel := context.WithTimeout(ctx, timeout)
		a := append([]string{"exec", l.ref(c.Name), "--"}, h.Argv...)
		_, err := l.run(hc, a...)
		cancel()
		if err != nil && !h.Optional {
			errs = append(errs, fmt.Errorf("hook failed: %w", err))
		}
	}
	return errors.Join(errs...)
}
func (e Engine) backup(ctx context.Context, hosts []Host, name string, concurrent bool) (result error) {
	if err := privateDir(e.C.StateDir); err != nil {
		return err
	}
	started := time.Now().UTC()
	defer func() {
		run := backupRun{Started: started, Completed: time.Now().UTC(), Success: result == nil}
		result = errors.Join(result, e.writeMetrics(&run))
	}()
	if len(hosts) == 0 {
		return errors.New("no hosts selected; use -local or inventory")
	}
	needsDestinations := false
	for _, host := range hosts {
		if host.SSH == "" {
			needsDestinations = true
		}
	}
	if needsDestinations && len(e.C.Destinations) == 0 {
		return errors.New("no backup destinations configured")
	}
	required := false
	for _, d := range e.C.Destinations {
		if !d.Optional {
			required = true
		}
	}
	if needsDestinations && !required {
		return errors.New("at least one required destination is needed for safe cleanup")
	}
	global := make(chan struct{}, e.C.Workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	add := func(err error) {
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}
	for _, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h.SSH != "" {
				select {
				case global <- struct{}{}:
				case <-ctx.Done():
					add(ctx.Err())
					return
				}
				defer func() { <-global }()
				agent := h.AgentBinary
				if agent == "" {
					agent = "lxcer"
				}
				command := shellQuote(agent) + " version >/dev/null && " + shellQuote(agent) + " -a backup -local -source-id " + shellQuote(h.Name) + " -config " + shellQuote(h.RemoteConfig)
				if name != "" {
					command += " -container " + shellQuote(name)
				}
				if concurrent {
					command += " -concurrently"
				}
				if _, err := runCommand(ctx, "ssh", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "--", h.SSH, command); err != nil {
					add(fmt.Errorf("host %s: %w", h.Name, err))
				}
				return
			}
			l := LXD{Host: h.Name}
			if err := l.Detect(ctx); err != nil {
				add(fmt.Errorf("host %s: %w", h.Name, err))
				return
			}
			cc, err := l.List(ctx)
			if err != nil {
				add(err)
				return
			}
			cc, err = selectContainers(cc, e.C.Blacklist, name)
			if err != nil {
				add(fmt.Errorf("host %s: %w", h.Name, err))
				return
			}
			workers := h.Workers
			if workers < 1 {
				workers = 1
			}
			limit := make(chan struct{}, workers)
			var jobs sync.WaitGroup
			for _, c := range cc {
				select {
				case limit <- struct{}{}:
				case <-ctx.Done():
					add(ctx.Err())
					jobs.Wait()
					return
				}
				select {
				case global <- struct{}{}:
				case <-ctx.Done():
					<-limit
					add(ctx.Err())
					jobs.Wait()
					return
				}
				jobs.Add(1)
				go func() {
					defer jobs.Done()
					defer func() { <-limit; <-global }()
					p := mergePolicy(e.C.Policy, h.Policy)
					p = mergePolicy(p, h.Instances[c.Name])
					if err := e.backupOne(ctx, l, c, p); err != nil {
						add(fmt.Errorf("backup %s/%s: %w", e.sourceHost(l), c.Name, err))
					}
				}()
			}
			jobs.Wait()
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (e Engine) sourceHost(l LXD) string {
	if e.C.HostID != "" && (l.Host == "local" || l.Host == "") {
		return e.C.HostID
	}
	if l.Host == "" {
		return "local"
	}
	return l.Host
}
func sourceRemote(m Manifest) string {
	if m.LXDRemote != "" {
		return m.LXDRemote
	}
	return m.Host
}
func (e Engine) backupOne(ctx context.Context, l LXD, c Container, p Policy) (result error) {
	if err := validatePolicy(p); err != nil {
		return err
	}
	if !validName(c.Name) {
		return errors.New("unsafe instance name from server")
	}
	lock, err := acquireLock(e.C.StateDir, l.Host+"/"+c.Name)
	if err != nil {
		return err
	}
	defer releaseLock(lock)
	journalPaths, err := manifestPaths(e.C.StateDir)
	if err != nil {
		return err
	}
	for _, path := range journalPaths {
		job, err := loadManifest(path)
		if err != nil {
			return fmt.Errorf("read backup journal %s: %w", filepath.Base(filepath.Dir(path)), err)
		}
		if sourceRemote(job) == l.Host && job.Instance.Name == c.Name && job.NeedsResume {
			return fmt.Errorf("%s still needs its resume hooks; run recover -execute", job.ID)
		}
	}
	id := newID()
	dir := filepath.Join(e.C.StateDir, id)
	if err = privateDir(dir); err != nil {
		return err
	}
	ctx, g, stopGuard := e.guard(ctx, dir)
	defer stopGuard()
	m := Manifest{Version: 1, ID: id, Host: e.sourceHost(l), LXDRemote: l.Host, Instance: c, Capabilities: l.Caps, Started: time.Now().UTC(), State: Running, Cleanup: "retained", Attachments: attachments(c)}
	log := e.Log.With("job_id", id, "host", m.Host, "instance", c.Name)
	cleanupReady := false
	defer func() {
		result = errors.Join(result, context.Cause(ctx))
		m.StagingPeakBytes = g.peak.Load()
		if result != nil {
			if !cleanupReady {
				if m.State == Verified {
					m.State = PartiallyCompleted
				} else if m.State != PartiallyCompleted {
					m.State = Failed
				}
			}
			m.Error = result.Error()
			log.Error("backup failed", "operation", "backup", "error", m.Error)
		}
		m.Completed = time.Now().UTC()
		if err := m.save(dir); err != nil {
			result = errors.Join(result, err)
		}
		if err := e.writeMetrics(nil); err != nil {
			log.Warn("unable to publish job metrics", "error", err)
		}
	}()
	if err = m.save(dir); err != nil {
		return err
	}
	if err = e.writeMetrics(nil); err != nil {
		log.Warn("unable to publish running job metrics", "error", err)
	}
	if strings.Contains(effectiveConfig(c)["raw.lxc"], "mount") && !p.AllowExcluded {
		return errors.New("raw.lxc mount configuration needs an explicit external-data policy")
	}
	if effectiveConfig(c)["raw.lxc"] != "" {
		log.Warn("raw LXC configuration requires independent dependency review")
	}
	if err = freeSpace(dir, e.C.MinFreeBytes); err != nil {
		return err
	}
	m.Storage, err = l.Storage(ctx, c)
	if err != nil {
		return err
	}
	if err = e.poolPreflight(ctx, l, c, &m); err != nil {
		return err
	}
	m.NativeRoot = p.NativeRoot
	for _, path := range append(append([]string{}, p.Paths...), p.NativeRoot) {
		if path != "" {
			if _, er := beneath(path, e.C.StateDir); er == nil {
				return errors.New("backup filesystem paths must not include state_dir")
			}
		}
	}
	m.Strategy, err = chooseStrategy(p, l.Caps, c)
	if err != nil {
		return err
	}
	if m.Strategy == "storage-incremental" {
		if er := e.streamPreflight(ctx, l, c, p, m.Storage); er != nil {
			if p.IncrementalFallback != "full" {
				return er
			}
			m.FallbackReason = er.Error()
			p.Strategy = "auto"
			m.Strategy, err = chooseStrategy(p, l.Caps, c)
			if err != nil {
				return err
			}
			log.Warn("explicit full fallback", "reason", m.FallbackReason)
		}
	}
	for i, a := range m.Attachments {
		if !a.Included {
			if filepath.IsAbs(a.Source) && (l.Host == "local" || l.Host == "") {
				for _, path := range p.Paths {
					canonicalSource, er := filepath.EvalSymlinks(a.Source)
					if er != nil {
						return er
					}
					rel, er := filepath.Rel(path, canonicalSource)
					if er == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
						m.Attachments[i].Included = true
						m.Attachments[i].Handling = "explicit native filesystem path"
					}
				}
			}
			if !m.Attachments[i].Included && p.BackupVolumes && a.Pool != "" && validName(a.Pool) && validName(a.Source) && l.Caps.Has("custom_volume_backup") {
				m.Attachments[i].Included = true
				m.Attachments[i].Handling = "separate managed volume export"
			}
			if !m.Attachments[i].Included && !p.AllowExcluded {
				return fmt.Errorf("disk %s (%s -> %s) is excluded: configure separate native backup or allow_excluded", a.Device, a.Source, a.Target)
			}
		}
	}
	for _, a := range m.Attachments {
		if !a.Included {
			log.Warn("storage excluded", "device", a.Device, "target", a.Target)
		}
	}
	if len(p.Paths) > 0 && l.Host != "local" && l.Host != "" {
		return errors.New("filesystem paths require execution on source host via SSH agent")
	}
	for _, path := range p.Paths {
		resolved, er := filepath.EvalSymlinks(path)
		if er != nil {
			return er
		}
		if resolved != filepath.Clean(path) {
			return errors.New("backup paths must use canonical paths without symlink components")
		}
		if _, err = os.Lstat(path); err != nil {
			return err
		}
	}
	if c.StatusCode == 103 && len(p.Paths) > 0 && len(p.Hooks.Quiesce) == 0 {
		return errors.New("running instance with direct filesystem paths requires explicit quiesce/resume hooks")
	}
	if err = e.hooks(ctx, l, c, p.Hooks.Pre); err != nil {
		return err
	}
	postDone := false
	resumed := false
	m.ResumeHooks = p.Hooks.Resume
	m.NeedsResume = len(p.Hooks.Quiesce) > 0
	if err = m.save(dir); err != nil {
		return err
	}
	resume := func() error {
		if resumed || len(p.Hooks.Resume) == 0 {
			return nil
		}
		rc, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		er := e.hooks(rc, l, c, p.Hooks.Resume)
		if er == nil {
			resumed = true
			m.NeedsResume = false
			return m.save(dir)
		}
		return er
	}
	defer func() {
		result = errors.Join(result, resume())
		pc, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if !postDone {
			result = errors.Join(result, e.hooks(pc, l, c, p.Hooks.Post))
		}
	}()
	if err = e.hooks(ctx, l, c, p.Hooks.Quiesce); err != nil {
		return err
	}
	captureStart := time.Now()
	switch m.Strategy {
	case "storage-incremental":
		if err = e.captureStream(ctx, l, c, p, &m, dir, func() error {
			if len(p.Paths) == 0 {
				return resume()
			}
			return nil
		}); err != nil {
			return err
		}
	case "native":
		if l.Host != "local" && l.Host != "" {
			return errors.New("native paths must run on source host using SSH agent mode")
		}
		if p.NativeRoot != "" {
			s, er := os.Lstat(p.NativeRoot)
			if er != nil {
				return er
			}
			if !s.IsDir() {
				return errors.New("native_root must be a directory")
			}
			resolved, er := filepath.EvalSymlinks(p.NativeRoot)
			if er != nil {
				return er
			}
			if resolved != filepath.Clean(p.NativeRoot) {
				return errors.New("native_root must be canonical")
			}
			p.Paths = append(p.Paths, p.NativeRoot)
		}
		for _, path := range p.Paths {
			if _, err = os.Stat(path); err != nil {
				return err
			}
		}
		m.Paths = append(m.Paths, p.Paths...)
	case "snapshot":
		m.Snapshot = id
		if err = m.save(dir); err != nil {
			return err
		}
		if _, err = l.run(ctx, "snapshot", l.ref(c.Name), id); err != nil {
			return err
		}
		m.State = SnapshotCreated
		if len(p.Paths) == 0 {
			if err = resume(); err != nil {
				return err
			}
		}
		m.ImageAlias = id
		if err = m.save(dir); err != nil {
			return err
		}
		if _, err = l.run(ctx, "publish", l.ref(c.Name+"/"+id), l.ref(""), "--alias", id, "--compression", "none"); err != nil {
			return err
		}

		aliasInfo, er := l.run(ctx, "query", l.ref("/1.0/images/aliases/"+id))
		if er != nil {
			return er
		}
		aliasInfo, er = unwrapAPI(aliasInfo)
		if er != nil {
			return er
		}
		var image struct {
			Target string `json:"target"`
		}
		if er = json.Unmarshal(aliasInfo, &image); er != nil {
			return er
		}
		if !snapshotID(image.Target) {
			return errors.New("image publication returned no fingerprint")
		}
		m.ImageFingerprint = image.Target
		if er = m.save(dir); er != nil {
			return er
		}
		stem := filepath.Join(dir, "image-"+id)
		if _, err = l.run(ctx, "image", "export", l.ref(id), stem); err != nil {
			return err
		}
		files, er := filepath.Glob(stem + "*")
		if er != nil {
			return er
		}

		if len(files) == 1 {
			m.Artifact = files[0]
			if e.C.Legacy && e.C.Compression == "zstd" {
				legacyArchive := filepath.Join(dir, c.Name+".tar")
				if err = os.Rename(m.Artifact, legacyArchive); err != nil {
					return err
				}
				m.Artifact = legacyArchive
			}
		} else if len(files) == 2 {
			meta, root := "", ""
			for _, file := range files {
				if file == stem+".root" {
					root = file
				} else {
					meta = file
				}
			}
			if meta == "" || root == "" {
				return errors.New("unrecognized split image layout; artifacts retained")
			}
			sum, size, er := checksum(ctx, root)
			if er != nil {
				return er
			}
			if size == 0 {
				return errors.New("empty split root filesystem")
			}
			m.Artifact = meta
			m.ImageParts = append(m.ImageParts, ImagePart{Artifact: root, SHA256: sum, Size: size})
			m.Paths = append(m.Paths, root)
		} else {
			return errors.New("image export produced no complete recoverable image")
		}

	case "incremental":
		mirror := LXD{Host: p.Mirror}
		if err = mirror.Detect(ctx); err != nil {
			return err
		}
		if !mirror.Caps.Export {
			return errors.New("incremental mirror requires portable export support")
		}
		mirrored, er := mirror.List(ctx)
		if er != nil {
			return er
		}
		target := "lxcer-" + m.Host + "-" + c.Name
		if !validName(target) {
			return errors.New("mirror name exceeds safe limit")
		}
		exists := false
		for _, x := range mirrored {
			if x.Name == target {
				exists = true
				if x.Config["user.lxcer.source"] != m.Host+"/"+c.Name {
					return errors.New("mirror collision with unowned instance")
				}
				if x.StatusCode != 102 {
					return errors.New("mirror must remain stopped")
				}
			}
		}
		if !exists {
			if _, err = l.run(ctx, "copy", l.ref(c.Name), mirror.ref(target), "--mode", "relay", "-c", "user.lxcer.source="+m.Host+"/"+c.Name, "-c", "boot.autostart=false"); err != nil {
				return err
			}
		} else {
			if _, err = l.run(ctx, "copy", l.ref(c.Name), mirror.ref(target), "--refresh", "--mode", "relay", "-c", "boot.autostart=false", "-c", "user.lxcer.source="+m.Host+"/"+c.Name); err != nil {
				return err
			}
		}
		if _, err = mirror.run(ctx, "config", "set", mirror.ref(target), "user.lxcer.source", m.Host+"/"+c.Name); err != nil {
			return err
		}
		if _, err = mirror.run(ctx, "config", "set", mirror.ref(target), "boot.autostart", "false"); err != nil {
			return err
		}
		m.Artifact = filepath.Join(dir, "backup.tar")
		if _, err = mirror.run(ctx, "export", mirror.ref(target), m.Artifact); err != nil {
			return err
		}
	case "full":
		m.Artifact = filepath.Join(dir, "backup.tar")
		if _, err = l.run(ctx, "export", l.ref(c.Name), m.Artifact); err != nil {
			return err
		}
	}

	m.CaptureSeconds = time.Since(captureStart).Seconds()
	if err = g.check(); err != nil {
		return err
	}
	for _, a := range m.Attachments {
		if a.Handling != "separate managed volume export" {
			continue
		}
		path := filepath.Join(dir, "volume-"+a.Device+".tar")
		if !validName(a.Device) {
			return errors.New("unsafe volume device name")
		}
		if _, err = l.run(ctx, "storage", "volume", "export", l.ref(a.Pool), a.Source, path); err != nil {
			return err
		}
		sum, size, er := checksum(ctx, path)
		if er != nil {
			return er
		}
		if size == 0 {
			return errors.New("empty managed volume export")
		}
		m.Volumes = append(m.Volumes, VolumeArtifact{Device: a.Device, Pool: a.Pool, Name: a.Source, Artifact: path, SHA256: sum, Size: size})
		m.Paths = append(m.Paths, path)
	}
	if m.Strategy != "native" {
		m.Paths = append(m.Paths, p.Paths...)
	}
	if m.Artifact != "" {
		if e.C.Compression == "zstd" {
			if _, err = runCommand(ctx, "zstd", "-T1", "--rsyncable", m.Artifact, "-o", m.Artifact+".zst"); err != nil {
				return err
			}
			m.Artifact += ".zst"
		}
		m.SHA256, m.Size, err = checksum(ctx, m.Artifact)
		if err != nil {
			return err
		}
		if err = g.check(); err != nil {
			return err
		}
		if m.Size == 0 {
			return errors.New("export produced empty artifact")
		}
		m.Paths = append(m.Paths, m.Artifact)
		if len(p.Paths) == 0 {
			if err = resume(); err != nil {
				return err
			}
		}
	}
	m.StagingPeakBytes = g.peak.Load()
	m.State = Exported
	if err = m.save(dir); err != nil {
		return err
	}
	paths := append(append([]string{}, m.Paths...), filepath.Join(dir, "manifest.json"))
	var uploadErrors []error
	requiredOK := true
	anyOK := false
	optionalFailed := false
	for _, d := range e.C.Destinations {
		r := e.repo(d)
		ds := DestinationStatus{Name: d.Name, Required: !d.Optional}
		tags := []string{"lxcer-data", "job=" + id, "host=" + m.Host, "instance=" + c.Name}
		if m.Stream != nil {
			tags = append(tags, "lxcer-chain", "chain="+m.Stream.BaseID)
		}
		err = e.optionalChainParents(ctx, m, d)
		if err == nil {
			ds.SnapshotID, ds.UploadBytes, err = r.backupMeasured(ctx, paths, tags)
		}
		if err == nil {
			err = r.verify(ctx, ds.SnapshotID, paths)
		}
		if err != nil {
			ds.Error = err.Error()
			uploadErrors = append(uploadErrors, fmt.Errorf("%s: %w", d.Name, err))
			if ds.Required {
				requiredOK = false
			} else {
				optionalFailed = true
			}
		} else {
			ds.Verified = true
			ds.IntegrityChecked = d.ReadData
			anyOK = true
		}
		m.Destinations = append(m.Destinations, ds)
		if er := m.save(dir); er != nil {
			return er
		}
	}
	if !requiredOK {
		if anyOK {
			m.State = PartiallyCompleted
		}
		return fmt.Errorf("required repository failed verification; artifacts retained: %w", errors.Join(uploadErrors...))
	}
	if err = resume(); err != nil {
		return err
	}
	pc, pcancel := context.WithTimeout(context.Background(), 5*time.Minute)
	err = e.hooks(pc, l, c, p.Hooks.Post)
	pcancel()
	postDone = true
	if err != nil {
		return err
	}
	// A receipt records verified data IDs. Keep the local job unfinished
	// until all receipts have been confirmed.
	for i, d := range e.C.Destinations {
		if !m.Destinations[i].Verified {
			continue
		}
		r := e.repo(d)
		receipt, er := r.backup(ctx, []string{filepath.Join(dir, "manifest.json")}, []string{"lxcer-receipt", "job=" + id, "host=" + m.Host, "instance=" + c.Name, "data=" + m.Destinations[i].SnapshotID})
		if er == nil {
			er = r.verify(ctx, receipt, []string{filepath.Join(dir, "manifest.json")})
		}
		if er != nil {
			m.Destinations[i].Error = "catalog receipt: " + er.Error()
			m.State = PartiallyCompleted
			return fmt.Errorf("catalog receipt failed for %s: %w", d.Name, er)
		}
		m.Destinations[i].ReceiptID = receipt
		if err = m.save(dir); err != nil {
			return err
		}
	}
	if optionalFailed {
		m.State = PartiallyCompleted
		return fmt.Errorf("optional repository failed verification; artifacts retained: %w", errors.Join(uploadErrors...))
	}
	m.State = Verified
	if err = m.save(dir); err != nil {
		return err
	}
	if m.Stream != nil {
		if err = e.commitStream(ctx, p, &m); err != nil {
			return err
		}
	}
	cleanupReady = true
	if err = e.cleanup(ctx, &m, dir); err != nil {
		return err
	}
	log.Info("backup verified", "operation", "backup", "size_bytes", m.Size, "strategy", m.Strategy)
	return nil
}
func (e Engine) cleanup(ctx context.Context, m *Manifest, dir string) error {
	if m.Legacy {
		return errors.New("legacy adoption has no job-owned cleanup")
	}
	if filepath.Base(dir) != m.ID || !strings.HasPrefix(m.ID, "lxcer-") {
		return errors.New("cleanup ownership mismatch")
	}
	if m.State != Verified {
		return errors.New("cleanup requires verified job; failure artifacts retained")
	}
	if m.NeedsResume || !m.verificationComplete() {
		return errors.New("cleanup requires completed resume hooks and verified required receipts")
	}
	if err := privateDir(dir); err != nil {
		return err
	}
	l := LXD{Host: sourceRemote(*m)}
	if m.Snapshot != "" {
		if m.Snapshot != m.ID {
			return errors.New("snapshot ownership mismatch")
		}
		endpoint := "containers"
		if m.Capabilities.InstanceEndpoint {
			endpoint = "instances"
		}
		path := "/1.0/" + endpoint + "/" + m.Instance.Name + "/snapshots"
		b, err := l.run(ctx, "query", l.ref(path))
		if err != nil {
			return err
		}
		if b, err = unwrapAPI(b); err != nil {
			return err
		}
		var snapshots []string
		if err = json.Unmarshal(b, &snapshots); err != nil {
			return err
		}
		// The server can return a project query parameter or the legacy resource path.
		for _, snapshot := range snapshots {
			resource, _, _ := strings.Cut(snapshot, "?")
			if !strings.HasSuffix(resource, "/"+m.Instance.Name+"/snapshots/"+m.ID) {
				continue
			}
			if _, err = l.run(ctx, "delete", l.ref(m.Instance.Name+"/"+m.ID)); err != nil {
				return err
			}
		}
	}
	if m.ImageAlias != "" {
		if m.ImageAlias != m.ID {
			return errors.New("image alias ownership mismatch")
		}
		b, err := l.run(ctx, "image", "alias", "list", l.ref(""), "--format", "json")
		if err != nil {
			return err
		}
		var aliases []struct {
			Name string `json:"name"`
		}
		if err = json.Unmarshal(b, &aliases); err != nil {
			return err
		}
		for _, alias := range aliases {
			if alias.Name != m.ID {
				continue
			}
			if _, err = l.run(ctx, "image", "alias", "delete", l.ref(m.ID)); err != nil {
				return err
			}
		}
	}
	// Never delete content-addressed images: another job may reference the same fingerprint.
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Name() == "manifest.json" {
			continue
		}
		if f.IsDir() {
			return errors.New("unexpected directory in job staging")
		}
	}
	for _, f := range files {
		if f.Name() == "manifest.json" {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = os.Remove(filepath.Join(dir, f.Name())); err != nil {
			return err
		}
	}
	m.Cleanup = "completed"
	return m.save(dir)
}

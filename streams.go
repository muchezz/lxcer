package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

type StreamEntry struct {
	ID           string            `json:"id"`
	Artifact     string            `json:"artifact"`
	SHA256       string            `json:"sha256"`
	Size         int64             `json:"size_bytes"`
	Snapshot     string            `json:"snapshot"`
	Repositories map[string]string `json:"repositories"`
}
type StreamBackup struct {
	Mode            string        `json:"mode"`
	Driver          string        `json:"driver"`
	Source          string        `json:"source"`
	RootRelative    string        `json:"root_relative"`
	Snapshot        string        `json:"snapshot"`
	BaseID          string        `json:"base_id"`
	Parents         []StreamEntry `json:"parents"`
	PreviousID      string        `json:"previous_id,omitempty"`
	SnapshotBytes   int64         `json:"snapshot_exclusive_bytes"`
	ReferencedBytes int64         `json:"referenced_bytes"`
}

var datasetPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*(/[a-zA-Z0-9][a-zA-Z0-9_.-]*)*$`)

func sourceKey(host, name string) string {
	h := sha256.Sum256([]byte(host + "/" + name))
	return hex.EncodeToString(h[:])
}
func beneath(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", errors.New("filesystem root escapes configured source")
	}
	return rel, nil
}
func (e Engine) snapshotRef(p Policy, driver, host, name, id string) string {
	if driver == "zfs" {
		return p.StorageSource + "@" + id
	}
	base := p.SnapshotDir
	if base == "" {
		base = filepath.Join(e.C.StateDir, "source-snapshots")
	}
	return filepath.Join(base, sourceKey(host, name), id)
}
func (e Engine) streamRoot(ctx context.Context, p Policy, driver string) (string, error) {
	source := p.StorageSource
	switch driver {
	case "btrfs":
		if !filepath.IsAbs(source) {
			return "", errors.New("btrfs storage_source must be an absolute subvolume path")
		}
		real, err := filepath.EvalSymlinks(source)
		if err != nil {
			return "", err
		}
		if real != filepath.Clean(source) {
			return "", errors.New("storage_source must be canonical")
		}
		if _, err = e.R.Run(ctx, nil, nil, "btrfs", "subvolume", "show", source); err != nil {
			return "", err
		}
		b, err := e.R.Run(ctx, nil, nil, "btrfs", "subvolume", "list", "-o", source)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(b)) != "" {
			return "", errors.New("nested Btrfs subvolumes need separate backup policies")
		}
		return source, nil
	case "zfs":
		if !datasetPattern.MatchString(source) {
			return "", errors.New("invalid ZFS dataset")
		}
		b, err := e.R.Run(ctx, nil, nil, "zfs", "get", "-H", "-o", "value", "mountpoint", source)
		if err != nil {
			return "", err
		}
		mount := strings.TrimSpace(string(b))
		if !filepath.IsAbs(mount) {
			// LXD commonly uses mountpoint=legacy and mounts the dataset itself.
			mounts, er := e.R.Run(ctx, nil, nil, "findmnt", "--json", "--source", source, "--output", "TARGET,FSROOT")
			if er != nil {
				return "", errors.New("zfs source is not mounted; configure a mounted source or explicit full fallback")
			}
			var table struct {
				Filesystems []struct {
					Target string `json:"target"`
					Root   string `json:"fsroot"`
				} `json:"filesystems"`
			}
			if er = json.Unmarshal(mounts, &table); er != nil {
				return "", er
			}
			mount = ""
			for _, fs := range table.Filesystems {
				if fs.Root == "/" && filepath.IsAbs(fs.Target) {
					if _, er = beneath(fs.Target, p.NativeRoot); er == nil && len(fs.Target) > len(mount) {
						mount = fs.Target
					}
				}
			}
			if mount == "" {
				return "", errors.New("zfs source has no mounted dataset root covering native_root")
			}
		}
		b, err = e.R.Run(ctx, nil, nil, "zfs", "list", "-H", "-o", "name", "-r", source)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(b)) != source {
			return "", errors.New("child ZFS datasets need separate backup policies")
		}
		return mount, nil
	}
	return "", fmt.Errorf("driver %s cannot produce supported filesystem incrementals; set incremental_fallback: full explicitly", driver)
}
func (e Engine) streamPreflight(ctx context.Context, l LXD, c Container, p Policy, driver string) error {
	if l.Host != "local" && l.Host != "" {
		return errors.New("storage streams must execute on the source using an SSH agent")
	}
	if c.Type == "virtual-machine" {
		return errors.New("filesystem streams do not reconstruct virtual machines")
	}
	if _, ok := e.R.(StreamingRunner); !ok {
		return errors.New("runner cannot stream filesystem data")
	}
	source, err := e.streamRoot(ctx, p, driver)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(p.NativeRoot)
	if err != nil {
		return err
	}
	if root != filepath.Clean(p.NativeRoot) {
		return errors.New("native_root must be canonical")
	}
	if _, err = beneath(source, root); err != nil {
		return err
	}
	if _, er := beneath(source, e.C.StateDir); er == nil {
		return errors.New("state_dir must be outside the streamed source")
	}
	if driver == "btrfs" {
		dir := filepath.Dir(e.snapshotRef(p, driver, e.sourceHost(l), c.Name, "probe"))
		if _, er := beneath(source, dir); er == nil {
			return errors.New("snapshot_dir must be outside the streamed source")
		}
		if err = privateDir(dir); err != nil {
			return err
		}
		a, err := os.Stat(source)
		if err != nil {
			return err
		}
		b, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if a.Sys().(*syscall.Stat_t).Dev != b.Sys().(*syscall.Stat_t).Dev {
			return errors.New("snapshot_dir must share the Btrfs source filesystem")
		}
	}
	return nil
}
func streamEntry(m Manifest) StreamEntry {
	repos := map[string]string{}
	for _, d := range m.Destinations {
		if d.Verified {
			repos[d.Name] = d.SnapshotID
		}
	}
	return StreamEntry{ID: m.ID, Artifact: m.Artifact, SHA256: m.SHA256, Size: m.Size, Snapshot: m.Stream.Snapshot, Repositories: repos}
}
func (e Engine) checkpointPath(host, name string) string {
	return filepath.Join(e.C.StateDir, "chains", sourceKey(host, name)+".json")
}
func validateStream(m Manifest) error {
	if m.Stream == nil {
		return errors.New("stream metadata missing")
	}
	s := m.Stream
	if (s.Driver != "btrfs" && s.Driver != "zfs") || !validName(s.BaseID) || len(s.Parents) > 127 {
		return errors.New("invalid stream chain")
	}
	if filepath.IsAbs(s.RootRelative) || s.RootRelative == ".." || strings.HasPrefix(s.RootRelative, "../") {
		return errors.New("unsafe stream root")
	}
	seen := map[string]bool{m.ID: true}
	for _, p := range s.Parents {
		if !validName(p.ID) || seen[p.ID] || !filepath.IsAbs(p.Artifact) || filepath.Base(filepath.Dir(p.Artifact)) != p.ID || len(p.SHA256) != 64 || p.Size <= 0 {
			return errors.New("invalid chain member")
		}
		seen[p.ID] = true
		for _, id := range p.Repositories {
			if !snapshotID(id) {
				return errors.New("invalid chain snapshot ID")
			}
		}
	}
	if len(s.Parents) > 0 && s.Parents[0].ID != s.BaseID {
		return errors.New("chain is missing its full base")
	}
	return nil
}
func (e Engine) captureStream(ctx context.Context, l LXD, c Container, p Policy, m *Manifest, dir string, afterSnapshot func() error) error {
	source, err := e.streamRoot(ctx, p, m.Storage)
	if err != nil {
		return err
	}
	relative, err := beneath(source, p.NativeRoot)
	if err != nil {
		return err
	}
	m.Stream = &StreamBackup{Mode: "full", Driver: m.Storage, Source: p.StorageSource, RootRelative: relative, Snapshot: e.snapshotRef(p, m.Storage, m.Host, c.Name, m.ID), BaseID: m.ID, ReferencedBytes: -1, SnapshotBytes: -1}
	checkpoint := e.checkpointPath(m.Host, c.Name)
	b, err := os.ReadFile(checkpoint)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var previous Manifest
	if err == nil {
		var id string
		if err = json.Unmarshal(b, &id); err != nil || !validName(id) {
			return errors.New("invalid source checkpoint")
		}
		previous, err = loadManifest(filepath.Join(e.C.StateDir, id, "manifest.json"))
		if err != nil {
			return err
		}
		if !recoverableCheckpoint(previous) || previous.Stream == nil || previous.Stream.Source != p.StorageSource || previous.Stream.Driver != m.Storage || previous.Host != m.Host || previous.Instance.Name != c.Name {
			return errors.New("source checkpoint identity mismatch")
		}
		if err = validateStream(previous); err != nil {
			return err
		}
		if previous.Stream.Snapshot != e.snapshotRef(p, m.Storage, m.Host, c.Name, id) {
			return errors.New("source snapshot ownership mismatch")
		}
		m.Stream.PreviousID = id
		max := p.MaxChain
		if max == 0 {
			max = 8
		}
		if len(previous.Stream.Parents)+1 < max {
			m.Stream.BaseID = previous.Stream.BaseID
			m.Stream.Parents = append(append([]StreamEntry{}, previous.Stream.Parents...), streamEntry(previous))
		}
	}

	var dependencyErr error
parents:
	for _, parent := range m.Stream.Parents {
		for _, d := range e.C.Destinations {
			if d.Optional {
				continue
			}
			id := parent.Repositories[d.Name]
			if !snapshotID(id) {
				dependencyErr = errors.New("required repository has no parent chain")
				break parents
			}
			if er := e.repo(d).verify(ctx, id, []string{parent.Artifact}); er != nil {
				dependencyErr = fmt.Errorf("parent chain is not recoverable: %w", er)
				break parents
			}
		}
	}
	if dependencyErr != nil {
		if p.IncrementalFallback != "full" {
			return dependencyErr
		}
		m.Stream.Parents = nil
		m.Stream.BaseID = m.ID
		m.FallbackReason = dependencyErr.Error() + "; explicit full stream restart"
		e.Log.Warn("restarting full native stream", "job_id", m.ID, "reason", m.FallbackReason)
	}
	if len(m.Stream.Parents) > 0 {
		m.Stream.Mode = "incremental"
	}

	if err = m.save(dir); err != nil {
		return err
	}
	var args []string
	command := ""
	if m.Storage == "btrfs" {
		if _, err = e.R.Run(ctx, nil, nil, "btrfs", "subvolume", "snapshot", "-r", p.StorageSource, m.Stream.Snapshot); err != nil {
			return err
		}
		command = "btrfs"
		args = []string{"send"}
		if len(m.Stream.Parents) > 0 {
			parent := m.Stream.Parents[len(m.Stream.Parents)-1]
			b, err = e.R.Run(ctx, nil, nil, "btrfs", "property", "get", "-ts", parent.Snapshot, "ro")
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(b)) != "ro=true" {
				return errors.New("parent snapshot must remain read-only")
			}
			args = append(args, "-p", parent.Snapshot)
		}
		args = append(args, m.Stream.Snapshot)
	} else {
		if _, err = e.R.Run(ctx, nil, nil, "zfs", "snapshot", m.Stream.Snapshot); err != nil {
			return err
		}
		command = "zfs"
		args = []string{"send"}
		if len(m.Stream.Parents) > 0 {
			args = append(args, "-i", m.Stream.Parents[len(m.Stream.Parents)-1].Snapshot)
		}
		args = append(args, m.Stream.Snapshot)
	}
	if err = afterSnapshot(); err != nil {
		return err
	}
	m.Artifact = filepath.Join(dir, "filesystem.stream")
	f, err := os.OpenFile(m.Artifact, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	writer := io.Writer(f)
	if e.C.MaxStagingBytes > 0 {
		writer = &boundedWriter{W: f, Remaining: e.C.MaxStagingBytes}
	}
	err = e.R.(StreamingRunner).RunStream(ctx, nil, nil, writer, command, args...)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}

	if m.Storage == "zfs" {
		if b, err = e.R.Run(ctx, nil, nil, "zfs", "get", "-Hp", "-o", "value", "referenced,used", m.Stream.Snapshot); err == nil {
			values := strings.Fields(string(b))
			if len(values) >= 2 {
				if n, er := strconv.ParseInt(values[0], 10, 64); er == nil {
					m.Stream.ReferencedBytes = n
				}
				if n, er := strconv.ParseInt(values[1], 10, 64); er == nil {
					m.Stream.SnapshotBytes = n
				}
			}
		}
	} else {
		if b, err = e.R.Run(ctx, nil, nil, "btrfs", "filesystem", "du", "-s", "--raw", m.Stream.Snapshot); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 3 {
					continue
				}
				n, er := strconv.ParseInt(fields[0], 10, 64)
				if er != nil {
					continue
				}
				m.Stream.ReferencedBytes = n
				if n, er = strconv.ParseInt(fields[1], 10, 64); er == nil {
					m.Stream.SnapshotBytes = n
				}
				break
			}
		}
	}

	return nil
}

type boundedWriter struct {
	W         io.Writer
	Remaining uint64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if uint64(len(p)) > w.Remaining {
		return 0, errors.New("stream exceeds staging byte limit")
	}
	n, err := w.W.Write(p)
	w.Remaining -= uint64(n)
	return n, err
}
func (e Engine) commitStream(ctx context.Context, p Policy, m *Manifest) error {
	if err := validateStream(*m); err != nil {
		return err
	}
	if err := privateDir(filepath.Join(e.C.StateDir, "chains")); err != nil {
		return err
	}
	b, _ := json.Marshal(m.ID)
	if err := atomicWrite(e.checkpointPath(m.Host, m.Instance.Name), b); err != nil {
		return err
	}
	// Only the latest verified source snapshot is needed for the next send; repository ancestors remain intact.
	if m.Stream.PreviousID != "" {
		old := e.snapshotRef(p, m.Storage, m.Host, m.Instance.Name, m.Stream.PreviousID)
		cmd := "btrfs"
		args := []string{"subvolume", "delete", old}
		if m.Storage == "zfs" {
			cmd = "zfs"
			args = []string{"destroy", old}
		}
		if _, err := e.R.Run(ctx, nil, nil, cmd, args...); err != nil {
			e.Log.Warn("verified chain retained; previous source snapshot cleanup failed", "job_id", m.ID)
		}
	}
	return nil
}

func recoverableCheckpoint(m Manifest) bool {
	if m.State != Verified && m.State != PartiallyCompleted {
		return false
	}
	required := 0
	for _, d := range m.Destinations {
		if d.Required {
			required++
		}
		if !d.Verified || !snapshotID(d.SnapshotID) || !snapshotID(d.ReceiptID) {
			return false
		}
	}
	return required > 0
}
func (e Engine) optionalChainParents(ctx context.Context, m Manifest, d Destination) error {
	if m.Stream == nil || !d.Optional {
		return nil
	}
	for _, parent := range m.Stream.Parents {
		id := parent.Repositories[d.Name]
		if !snapshotID(id) {
			return errors.New("optional repository lacks a recoverable parent chain")
		}
		if err := e.repo(d).verify(ctx, id, []string{parent.Artifact}); err != nil {
			return err
		}
	}
	return nil
}

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type State string

const (
	Running            State = "Running"
	SnapshotCreated    State = "SnapshotCreated"
	Exported           State = "Exported"
	Verified           State = "Verified"
	Failed             State = "Failed"
	PartiallyCompleted State = "PartiallyCompleted"
)

type DestinationStatus struct {
	UploadBytes      int64  `json:"upload_bytes"`
	Name             string `json:"name"`
	Required         bool   `json:"required"`
	SnapshotID       string `json:"snapshot_id,omitempty"`
	ReceiptID        string `json:"receipt_id,omitempty"`
	Verified         bool   `json:"verified"`
	IntegrityChecked bool   `json:"integrity_checked"`
	Error            string `json:"error,omitempty"`
}
type RestoreTest struct {
	Time      time.Time `json:"time"`
	Duration  float64   `json:"duration_seconds"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	BackupAge float64   `json:"backup_age_seconds"`
}
type VolumeArtifact struct {
	Device   string `json:"device"`
	Pool     string `json:"pool"`
	Name     string `json:"name"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size_bytes"`
}
type ImagePart struct {
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size_bytes"`
}
type Manifest struct {
	PoolFreeBytes      int64               `json:"pool_free_bytes"`
	PoolPreflightError string              `json:"pool_preflight_error,omitempty"`
	LXDRemote          string              `json:"lxd_remote,omitempty"`
	ImageParts         []ImagePart         `json:"image_parts,omitempty"`
	NativeRoot         string              `json:"native_root,omitempty"`
	Stream             *StreamBackup       `json:"stream,omitempty"`
	FallbackReason     string              `json:"fallback_reason,omitempty"`
	CaptureSeconds     float64             `json:"capture_seconds"`
	StagingPeakBytes   int64               `json:"staging_peak_bytes"`
	ImageFingerprint   string              `json:"image_fingerprint,omitempty"`
	Legacy             bool                `json:"legacy,omitempty"`
	Volumes            []VolumeArtifact    `json:"volumes,omitempty"`
	NeedsResume        bool                `json:"needs_resume"`
	ResumeHooks        []Hook              `json:"resume_hooks,omitempty"`
	Version            int                 `json:"version"`
	ID                 string              `json:"id"`
	Host               string              `json:"host"`
	Instance           Container           `json:"instance"`
	Capabilities       Capabilities        `json:"capabilities"`
	Storage            string              `json:"storage"`
	Strategy           string              `json:"strategy"`
	State              State               `json:"state"`
	Started            time.Time           `json:"started"`
	Completed          time.Time           `json:"completed,omitempty"`
	Size               int64               `json:"size_bytes"`
	SHA256             string              `json:"sha256,omitempty"`
	Artifact           string              `json:"artifact,omitempty"`
	Paths              []string            `json:"paths,omitempty"`
	Attachments        []Attachment        `json:"attachments"`
	Destinations       []DestinationStatus `json:"destinations"`
	Error              string              `json:"error,omitempty"`
	Cleanup            string              `json:"cleanup"`
	Snapshot           string              `json:"snapshot,omitempty"`
	ImageAlias         string              `json:"image_alias,omitempty"`
	RestoreTest        *RestoreTest        `json:"restore_test,omitempty"`
}

func newID() string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return "lxcer-" + hex.EncodeToString(b[:])
}
func privateDir(path string) error {
	if e := os.MkdirAll(path, 0700); e != nil {
		return e
	}
	s, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !s.IsDir() || s.Mode().Perm()&0077 != 0 {
		return errors.New("state directory must be a private directory (0700)")
	}
	return nil
}
func atomicWrite(path string, b []byte) error { return atomicWriteMode(path, b, 0600) }
func atomicWriteMode(path string, b []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".write-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (m *Manifest) save(dir string) error {
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(dir, "manifest.json"), b)
}
func loadManifest(path string) (Manifest, error) {
	var m Manifest
	b, e := os.ReadFile(path)
	if e != nil {
		return m, e
	}
	e = json.Unmarshal(b, &m)
	if e == nil && (m.Version != 1 || !validName(m.ID) || !validName(m.Instance.Name)) {
		e = errors.New("invalid manifest")
	}
	if e == nil {
		if !validName(m.Host) {
			return m, errors.New("invalid manifest host")
		}
		if !m.Legacy && m.Artifact != "" && (!filepath.IsAbs(m.Artifact) || filepath.Base(filepath.Dir(m.Artifact)) != m.ID) {
			return m, errors.New("artifact ownership mismatch")
		}
		if m.LXDRemote != "" && !validName(m.LXDRemote) {
			return m, errors.New("invalid source remote")
		}
		if m.Stream != nil {
			if err := validateStream(m); err != nil {
				return m, err
			}
		}
		destinations := make(map[string]bool, len(m.Destinations))
		for _, d := range m.Destinations {
			if !validName(d.Name) || destinations[d.Name] {
				return m, errors.New("invalid or duplicate manifest destination")
			}
			destinations[d.Name] = true
		}
		for _, v := range m.Volumes {
			if !validName(v.Device) || !validName(v.Name) || !validName(v.Pool) || !filepath.IsAbs(v.Artifact) || filepath.Base(filepath.Dir(v.Artifact)) != m.ID {
				return m, errors.New("invalid volume artifact")
			}
		}
	}
	return m, e
}

// verificationComplete applies to scheduled backups. A legacy adoption has no receipt.
func (m Manifest) verificationComplete() bool {
	required := false
	for _, d := range m.Destinations {
		if d.Required {
			required = true
			if !d.Verified || !snapshotID(d.SnapshotID) || !snapshotID(d.ReceiptID) {
				return false
			}
		}
	}
	return required
}

func manifestPaths(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() || !validName(entry.Name()) || !strings.HasPrefix(entry.Name(), "lxcer-") {
			continue
		}
		path := filepath.Join(root, entry.Name(), "manifest.json")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: expected a regular manifest", entry.Name())
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func checksum(ctx context.Context, path string) (string, int64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errors.New("backup artifact must be a regular file")
	}
	h := sha256.New()
	var size int64
	buf := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", size, err
		}
		n, err := f.Read(buf)
		_, _ = h.Write(buf[:n])
		size += int64(n)
		if err == io.EOF {
			return hex.EncodeToString(h.Sum(nil)), size, nil
		}
		if err != nil {
			return "", size, err
		}
	}
}

var errJobLocked = errors.New("job already locked; another process is active")

func acquireLock(root, key string) (*os.File, error) { return acquireLockMode(root, key, false) }
func openLock(root, key string) (*os.File, error) {
	if err := privateDir(filepath.Join(root, "locks")); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(key))
	path := filepath.Join(root, "locks", hex.EncodeToString(sum[:])+".lock")
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
}

func acquireLockMode(root, key string, blocking bool) (*os.File, error) {
	f, err := openLock(root, key)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_EX
	if !blocking {
		mode |= syscall.LOCK_NB
	}
	if err = syscall.Flock(int(f.Fd()), mode); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errJobLocked
		}
		return nil, err
	}
	return f, nil
}

func releaseLock(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

func freeSpace(path string, min uint64) error {
	var s syscall.Statfs_t
	if e := syscall.Statfs(path, &s); e != nil {
		return e
	}
	if s.Bavail*uint64(s.Bsize) < min {
		return errors.New("insufficient staging disk space")
	}
	return nil
}

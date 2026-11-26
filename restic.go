package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Repository struct {
	D         Destination
	Retries   int
	Bandwidth int
	StateDir  string
}

func (r Repository) env() ([]string, error) {
	env := []string{"RESTIC_REPOSITORY=" + r.D.Path, "RESTIC_REPOSITORY_FILE="}
	if r.D.PasswordFile != "" || r.D.PasswordEnv != "" || r.D.Password != "" {
		env = append(env, "RESTIC_PASSWORD_COMMAND=")
	}
	switch {
	case r.D.PasswordFile != "":
		env = append(env, "RESTIC_PASSWORD_FILE="+r.D.PasswordFile, "RESTIC_PASSWORD=")
	case r.D.PasswordEnv != "":
		p, ok := os.LookupEnv(r.D.PasswordEnv)
		if !ok || p == "" {
			return nil, errors.New("password environment variable missing or empty")
		}
		env = append(env, "RESTIC_PASSWORD="+p, "RESTIC_PASSWORD_FILE=")
	case r.D.Password != "":
		env = append(env, "RESTIC_PASSWORD="+r.D.Password, "RESTIC_PASSWORD_FILE=")
	}
	return env, nil
}

// Restic checks and retention need an exclusive lock. Coordinate local
// lxcer processes so they do not repeatedly race with concurrent uploads.
func (r Repository) lock(ctx context.Context, exclusive bool) (*os.File, error) {
	info, err := os.Lstat(r.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		// Repository-only commands can run before a local catalog exists.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, nil
	}

	key := r.D.Path
	localPath, explicitLocal := strings.CutPrefix(key, "local:")
	if explicitLocal || !strings.Contains(key, ":") {
		absolute, err := filepath.Abs(localPath)
		if err != nil {
			return nil, err
		}
		if canonical, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = canonical
		}
		key = "local:" + absolute
	}
	f, err := openLock(r.StateDir, "restic:"+key)
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS) {
		// A catalog on a read-only mount can rely on Restic's own locking.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r Repository) run(ctx context.Context, args ...string) ([]byte, error) {
	env, e := r.env()
	if e != nil {
		return nil, e
	}
	lock, err := r.lock(ctx, args[0] == "check" || args[0] == "forget")
	if err != nil {
		return nil, err
	}
	defer releaseLock(lock)

	command := append([]string{"--retry-lock", "1m"}, args...)
	var b []byte
	for i := 0; i <= r.Retries; i++ {
		b, e = runOutput(ctx, env, nil, "restic", command...)
		if e == nil {
			return b, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if i < r.Retries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(i+1) * time.Second):
			}
		}
	}
	return nil, fmt.Errorf("destination %s: %s: %w", r.D.Name, args[0], e)
}
func (r Repository) backup(ctx context.Context, paths []string, tags []string) (string, error) {
	id, _, err := r.backupMeasured(ctx, paths, tags)
	return id, err
}
func (r Repository) backupMeasured(ctx context.Context, paths []string, tags []string) (string, int64, error) {
	args := []string{"backup", "--json", "--quiet"}
	for _, tag := range tags {
		args = append(args, "--tag", tag)
	}
	if r.Bandwidth > 0 {
		args = append(args, "--limit-upload", strconv.Itoa(r.Bandwidth))
	}
	args = append(args, "--")
	args = append(args, paths...)
	b, e := r.run(ctx, args...)
	if e != nil {
		return "", 0, e
	}
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 4096), 1<<20)
	id := ""
	var uploaded int64
	for s.Scan() {
		var row struct {
			Type  string `json:"message_type"`
			ID    string `json:"snapshot_id"`
			Bytes int64  `json:"data_added_packed"`
		}
		if e = json.Unmarshal(s.Bytes(), &row); e != nil {
			return "", 0, e
		}
		if row.Type == "summary" {
			id = row.ID
			uploaded = row.Bytes
		}
	}
	if e = s.Err(); e != nil {
		return "", 0, e
	}
	if !snapshotID(id) {
		return "", 0, errors.New("restic returned no valid snapshot ID")
	}
	// Older Restic versions report a short ID in the backup summary.
	if len(id) != 64 {
		snap, err := r.snapshot(ctx, id)
		if err != nil {
			return "", 0, err
		}
		id = snap.ID
	}
	return id, uploaded, nil
}
func snapshotID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

type repositorySnapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
}

func (r Repository) snapshot(ctx context.Context, id string) (repositorySnapshot, error) {
	if !snapshotID(id) {
		return repositorySnapshot{}, errors.New("invalid snapshot id")
	}
	b, err := r.run(ctx, "snapshots", "--json", id)
	if err != nil {
		return repositorySnapshot{}, err
	}
	var snaps []repositorySnapshot
	if err = json.Unmarshal(b, &snaps); err != nil {
		return repositorySnapshot{}, err
	}
	if len(snaps) != 1 || !strings.HasPrefix(snaps[0].ID, id) {
		return repositorySnapshot{}, errors.New("snapshot missing or ambiguous")
	}
	return snaps[0], nil
}

func (r Repository) restorePath(ctx context.Context, id, target, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
		return errors.New("restore requires an absolute snapshot path below /")
	}
	// Restic --include uses glob syntax. Escape the recorded literal path.
	pattern := strings.NewReplacer(`\`, `\`, "*", `\*`, "?", `\?`, "[", `\[`).Replace(filepath.Clean(path))
	_, err := r.run(ctx, "restore", id, "--target", target, "--include", pattern, "--verify")
	return err
}

func (r Repository) verify(ctx context.Context, id string, paths []string) error {
	snapshot, e := r.snapshot(ctx, id)
	if e != nil {
		return e
	}
	for _, p := range paths {
		found := false
		for _, q := range snapshot.Paths {
			if p == q {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("snapshot missing expected path %s", p)
		}
		if e = r.verifyNode(ctx, snapshot.ID, p); e != nil {
			return e
		}
	}
	if r.D.Check || r.D.ReadData {
		args := []string{"check"}
		if r.D.ReadData {
			args = append(args, "--read-data")
		}
		_, e = r.run(ctx, args...)
	}
	return e
}

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

// restoreLegacy retains the original flag/config/archive workflow while adding
// verification, collision checks, bounded execution and retained failure staging.
func (e Engine) restoreLegacy(ctx context.Context, source, target, host, destination, id string, start bool) (result error) {
	if !validName(source) || !validName(target) || !validName(host) {
		return errors.New("invalid legacy restore selection")
	}
	d, err := e.destination(destination)
	if err != nil {
		return err
	}
	r := e.repo(d)
	l := LXD{R: e.R, Host: host}
	cc, err := l.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range cc {
		if c.Name == target {
			return errors.New("restore collision: instance already exists")
		}
	}
	if err = privateDir(e.C.StateDir); err != nil {
		return err
	}
	lock, err := acquireLock(e.C.StateDir, host+"/"+target)
	if err != nil {
		return err
	}
	defer releaseLock(lock)
	args := []string{"snapshots", "--json"}
	if id != "" {
		if !snapshotID(id) {
			return errors.New("invalid snapshot ID")
		}
		args = append(args, id)
	}
	b, err := r.run(ctx, args...)
	if err != nil {
		return err
	}
	var snapshots []struct {
		ID    string    `json:"id"`
		Time  time.Time `json:"time"`
		Paths []string  `json:"paths"`
	}
	if err = json.Unmarshal(b, &snapshots); err != nil {
		return err
	}
	artifact := ""
	latest := time.Time{}
	for _, s := range snapshots {
		for _, path := range s.Paths {
			if filepath.Base(path) == source+".tar.zst" && filepath.IsAbs(path) && (artifact == "" || s.Time.After(latest)) {
				artifact = path
				id = s.ID
				latest = s.Time
			}
		}
	}
	if artifact == "" {
		return fmt.Errorf("no legacy archive found for %s", source)
	}
	if err = r.verify(ctx, id, []string{artifact}); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(e.C.StateDir, "restore-")
	if err != nil {
		return err
	}
	job := newID()
	journal := map[string]any{"version": 1, "id": job, "legacy": true, "source": source, "host": host, "target": target, "snapshot_id": id, "backup_time": latest, "artifact": artifact, "started": time.Now().UTC(), "state": "Running"}
	save := func() error {
		b, er := json.MarshalIndent(journal, "", "  ")
		if er != nil {
			return er
		}
		return atomicWrite(filepath.Join(dir, "restore.json"), b)
	}
	if err = save(); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			journal["state"] = "Failed"
			journal["error"] = result.Error()
			result = errors.Join(result, save())
		} else {
			result = os.RemoveAll(dir)
		}
	}()
	if _, err = r.run(ctx, "restore", id, "--target", dir, "--include", artifact, "--verify"); err != nil {
		return err
	}
	restored := filepath.Join(dir, strings.TrimPrefix(filepath.Clean(artifact), "/"))
	if _, _, err = checksum(restored); err != nil {
		return err
	}
	tar := filepath.Join(dir, source+".tar")
	if _, err = e.R.Run(ctx, nil, nil, "zstd", "-d", "-T1", restored, "-o", tar); err != nil {
		return err
	}
	if _, err = l.run(ctx, "image", "import", tar, l.ref(""), "--alias", job); err != nil {
		return err
	}
	if _, err = l.run(ctx, "init", l.ref(job), l.ref(target), "-c", "user.lxcer.restore="+job); err != nil {
		return err
	}
	cc, err = l.List(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, c := range cc {
		if c.Name == target {
			found = true
			if c.StatusCode != 102 || c.Config["user.lxcer.restore"] != job {
				return errors.New("legacy restore instance validation failed")
			}
		}
	}
	if !found {
		return errors.New("legacy restored instance missing")
	}
	if start {
		if _, err = l.run(ctx, "start", l.ref(target)); err != nil {
			return err
		}
	}
	// Delete only our alias; shared image content and failure resources remain safe.
	if _, err = l.run(ctx, "image", "alias", "delete", l.ref(job)); err != nil {
		return err
	}
	return nil
}

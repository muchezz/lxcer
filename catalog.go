package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var errNoCatalog = errors.New("no verified catalog entry for requested source")

func (e Engine) findManifest(name, host, destination, snapshot string) (Manifest, string, error) {
	files, err := manifestPaths(e.C.StateDir)
	if err != nil {
		return Manifest{}, "", err
	}
	var found Manifest
	selected := ""
	for _, f := range files {
		m, er := loadManifest(f)
		if er != nil {
			return found, "", er
		}
		if m.Instance.Name != name || (host != "" && m.Host != host) || (m.State != Verified && m.State != PartiallyCompleted) {
			continue
		}
		if destination != "" && snapshot == "" && !slices.ContainsFunc(m.Destinations, func(d DestinationStatus) bool {
			return d.Name == destination && d.Verified && snapshotID(d.SnapshotID)
		}) {
			continue
		}
		if selected != "" && m.Host != found.Host && host == "" {
			return found, "", errors.New("source name exists on multiple hosts; specify -source-host")
		}
		if selected == "" || m.Started.After(found.Started) {
			found = m
			selected = f
		}
	}
	if selected == "" {
		return found, "", errNoCatalog
	}
	return found, selected, nil
}

// Adopt an old image archive without inventing missing instance metadata.
func (e Engine) adoptLegacy(ctx context.Context, destination, id, artifact, metadata, host string) (Manifest, error) {
	var m Manifest
	if !validName(host) || !snapshotID(id) || !filepath.IsAbs(artifact) || artifact == "/" {
		return m, errors.New("legacy adoption needs source host, snapshot ID and original absolute archive path")
	}
	b, err := os.ReadFile(metadata)
	if err != nil {
		return m, err
	}
	var c Container
	if err = json.Unmarshal(b, &c); err != nil {
		return m, err
	}
	if !validName(c.Name) || c.Architecture == "" {
		return m, errors.New("metadata must be a JSON instance object, including name and architecture")
	}
	d, err := e.destination(destination)
	if err != nil {
		return m, err
	}
	r := e.repo(d)
	if err = r.verify(ctx, id, []string{artifact}); err != nil {
		return m, err
	}

	snap, err := r.snapshot(ctx, id)
	if err != nil {
		return m, err
	}
	if snap.Time.IsZero() {
		return m, errors.New("legacy snapshot timestamp unavailable; cannot assign a reliable backup age")
	}
	capturedAt := snap.Time
	if err = privateDir(e.C.StateDir); err != nil {
		return m, err
	}
	staging, err := os.MkdirTemp(e.C.StateDir, "adopt-")
	if err != nil {
		return m, err
	}
	ctx, guard, stop := e.guard(ctx, staging)
	defer stop()
	if err = guard.check(); err != nil {
		return m, err
	}
	if err = r.restorePath(ctx, id, staging, artifact); err != nil {
		return m, err
	}
	sum, size, err := checksum(ctx, filepath.Join(staging, strings.TrimPrefix(filepath.Clean(artifact), "/")))
	if err != nil {
		return m, err
	}
	if err = guard.check(); err != nil {
		return m, err
	}
	if err = ctx.Err(); err != nil {
		return m, err
	}
	if size == 0 {
		return m, errors.New("legacy archive is empty")
	}
	jobID := newID()
	dir := filepath.Join(e.C.StateDir, jobID)
	if err = privateDir(dir); err != nil {
		return m, err
	}
	// The original archive path is retained for Restic lookup; no job-owned cleanup applies.
	m = Manifest{Version: 1, ID: jobID, Host: host, Instance: c, Strategy: "snapshot", State: Verified, Started: capturedAt, Completed: capturedAt, Artifact: artifact, Paths: []string{artifact}, SHA256: sum, Size: size, Attachments: attachments(c), Destinations: []DestinationStatus{{Name: d.Name, Required: true, SnapshotID: id, Verified: true}}, Cleanup: "legacy artifact; no cleanup authorization", Legacy: true}
	if err = m.save(dir); err != nil {
		return m, err
	}
	return m, os.RemoveAll(staging)
}

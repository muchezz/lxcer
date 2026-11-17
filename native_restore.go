package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type idRange struct {
	Isuid    bool
	Isgid    bool
	Hostid   int64
	Nsid     int64
	Maprange int64
}

func nativeIDMaps(m Manifest) ([]idRange, error) {
	cfg := effectiveConfig(m.Instance)
	for _, key := range []string{"volatile.last_state.idmap", "volatile.idmap.current"} {
		if cfg[key] != "" {
			var ranges []idRange
			if err := json.Unmarshal([]byte(cfg[key]), &ranges); err != nil {
				return nil, err
			}
			for _, r := range ranges {
				if r.Hostid < 0 || r.Nsid < 0 || r.Maprange <= 0 || r.Hostid+r.Maprange > 1<<32 || r.Nsid+r.Maprange > 1<<32 {
					return nil, errors.New("invalid source id map")
				}
			}
			return ranges, nil
		}
	}
	return nil, nil
}
func mappedID(id uint32, uid bool, ranges []idRange) (uint32, error) {
	if len(ranges) == 0 {
		return id, nil
	}
	for _, r := range ranges {
		if ((uid && r.Isuid) || (!uid && r.Isgid)) && int64(id) >= r.Hostid && int64(id) < r.Hostid+r.Maprange {
			return uint32(int64(id) - r.Hostid + r.Nsid), nil
		}
	}
	return 0, errors.New("source file owner is outside captured LXD id map")
}
func prepareOwnerMaps(root, dir string, m Manifest) (string, string, error) {
	ranges, err := nativeIDMaps(m)
	if err != nil {
		return "", "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", errors.New("native root is not a directory")
	}
	if len(ranges) == 0 && info.Sys().(*syscall.Stat_t).Uid != 0 {
		return "", "", errors.New("native root is shifted or user-owned but manifest has no id map; use a qualified unshifted root or image strategy")
	}
	users := map[uint32]uint32{}
	groups := map[uint32]uint32{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		u, err := mappedID(st.Uid, true, ranges)
		if err != nil {
			return err
		}
		g, err := mappedID(st.Gid, false, ranges)
		if err != nil {
			return err
		}
		users[st.Uid] = u
		groups[st.Gid] = g
		if len(ranges) > 0 && d.Type()&os.ModeSymlink == 0 {
			for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default", "security.capability"} {
				n, er := syscall.Getxattr(path, name, nil)
				if er == syscall.ENODATA || er == syscall.ENOTSUP {
					continue
				}
				if er != nil {
					return er
				}
				b := make([]byte, n)
				if _, er = syscall.Getxattr(path, name, b); er != nil {
					return er
				}
				normalized, changed, er := unshiftXattr(name, b, ranges)
				if er != nil {
					return er
				}
				if changed {
					if er = syscall.Setxattr(path, name, normalized, 0); er != nil {
						return fmt.Errorf("restore needs privileges to normalize %s on private staging: %w", name, er)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	write := func(name string, ids map[uint32]uint32) (string, error) {
		var b strings.Builder
		for from, to := range ids {
			fmt.Fprintf(&b, "+%d +%d\n", from, to)
		}
		path := filepath.Join(dir, name)
		return path, os.WriteFile(path, []byte(b.String()), 0600)
	}
	owner, err := write("owner.map", users)
	if err != nil {
		return "", "", err
	}
	group, err := write("group.map", groups)
	return owner, group, err
}
func (e Engine) nativeImage(ctx context.Context, root, dir string, m Manifest) (string, error) {
	if m.Instance.Type == "virtual-machine" || m.Instance.Architecture == "" {
		return "", errors.New("native image reconstruction requires container architecture metadata")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if _, err = beneath(dir, realRoot); err != nil || realRoot == filepath.Clean(dir) {
		return "", errors.New("native reconstruction root escapes private staging")
	}
	root = realRoot
	owner, group, err := prepareOwnerMaps(root, dir, m)
	if err != nil {
		return "", err
	}
	b, err := yaml.Marshal(map[string]any{"architecture": m.Instance.Architecture, "creation_date": m.Started.Unix(), "properties": map[string]string{"description": "lxcer recovery " + m.ID}})
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(dir, "metadata.yaml"), b, 0600); err != nil {
		return "", err
	}
	image := filepath.Join(dir, "native-image.tar")
	// Transform member/hardlink names, leaving symlink targets untouched. GNU tar preserves sparse files/ACLs/xattrs.
	_, err = e.R.Run(ctx, nil, nil, "tar", "--create", "--file", image, "--numeric-owner", "--sparse", "--acls", "--xattrs", "--xattrs-include=*", "--owner-map="+owner, "--group-map="+group, "--transform=flags=rh;s,^\\./,rootfs/,;s,^\\.$,rootfs,", "-C", dir, "metadata.yaml", "-C", root, ".")
	return image, err
}
func (e Engine) reconstructNative(ctx context.Context, r Repository, id, staging string, m Manifest, opt RestoreOptions) (string, func() error, error) {
	none := func() error { return nil }
	if m.Strategy == "storage-incremental" {
		return e.receiveStream(ctx, r, id, staging, m, opt)
	}
	root := m.NativeRoot
	if root == "" && len(m.Paths) > 0 {
		root = m.Paths[0]
		for _, p := range m.Paths {
			if p != root {
				return "", none, errors.New("old native manifest has ambiguous root; use -native-target")
			}
		}
	}
	if !filepath.IsAbs(root) || root == "/" {
		return "", none, errors.New("native manifest has no safe root path")
	}
	if err := r.verify(ctx, id, m.Paths); err != nil {
		return "", none, err
	}
	if _, err := r.run(ctx, "restore", id, "--target", staging, "--verify"); err != nil {
		return "", none, err
	}
	restored := filepath.Join(staging, strings.TrimPrefix(filepath.Clean(root), "/"))
	return restored, none, nil
}
func (e Engine) receiveStream(ctx context.Context, r Repository, currentID, staging string, m Manifest, opt RestoreOptions) (string, func() error, error) {
	none := func() error { return nil }
	if err := validateStream(m); err != nil {
		return "", none, err
	}
	sr, ok := e.R.(StreamingRunner)
	if !ok {
		return "", none, errors.New("runner cannot receive filesystem streams")
	}
	entries := append(append([]StreamEntry{}, m.Stream.Parents...), streamEntry(m))
	entries[len(entries)-1].Repositories = map[string]string{r.D.Name: currentID}
	received := filepath.Join(staging, "received")
	if err := privateDir(received); err != nil {
		return "", none, err
	}
	dataset := ""
	created := false
	cleanup := func() error {
		if !created {
			return nil
		}
		cc, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if m.Stream.Driver == "zfs" {
			b, err := e.R.Run(cc, nil, nil, "zfs", "get", "-H", "-o", "value", "org.lxcer:restore", dataset)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(b)) != filepath.Base(staging) {
				return errors.New("receive dataset ownership mismatch")
			}
			_, err = e.R.Run(cc, nil, nil, "zfs", "destroy", "-r", dataset)
			return err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			path := filepath.Join(received, entries[i].ID)
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if _, err := e.R.Run(cc, nil, nil, "btrfs", "subvolume", "delete", path); err != nil {
				return err
			}
		}
		return nil
	}
	if m.Stream.Driver == "zfs" {
		if !datasetPattern.MatchString(opt.ReceiveDataset) {
			return "", none, errors.New("ZFS restore requires -receive-dataset pointing at a disposable parent dataset")
		}
		dataset = opt.ReceiveDataset + "/" + filepath.Base(staging)
		if _, err := e.R.Run(ctx, nil, nil, "zfs", "list", "-H", dataset); err == nil {
			return "", none, errors.New("receive dataset collision")
		}
	}
	for i, entry := range entries {
		id := entry.Repositories[r.D.Name]
		if !snapshotID(id) {
			return "", cleanup, errors.New("chain member absent from chosen destination")
		}
		if err := r.verify(ctx, id, []string{entry.Artifact}); err != nil {
			return "", cleanup, err
		}
		if _, err := r.run(ctx, "restore", id, "--target", staging, "--include", entry.Artifact, "--verify"); err != nil {
			return "", cleanup, err
		}
		path := filepath.Join(staging, strings.TrimPrefix(filepath.Clean(entry.Artifact), "/"))
		sum, _, err := checksum(path)
		if err != nil {
			return "", cleanup, err
		}
		if sum != entry.SHA256 {
			return "", cleanup, errors.New("chain stream checksum mismatch")
		}
		if strings.HasSuffix(path, ".zst") {
			if _, err = e.R.Run(ctx, nil, nil, "zstd", "-d", "-T1", path, "-o", path+".raw"); err != nil {
				return "", cleanup, err
			}
			path += ".raw"
		}
		f, err := os.Open(path)
		if err != nil {
			return "", cleanup, err
		}
		if m.Stream.Driver == "btrfs" {
			err = sr.RunStream(ctx, nil, f, nil, "btrfs", "receive", "-e", received)
			created = true
		} else {
			args := []string{"receive", "-u"}
			if i == 0 {
				args = append(args, "-o", "mountpoint="+received, "-o", "canmount=noauto", "-o", "readonly=on", "-o", "org.lxcer:restore="+filepath.Base(staging))
			}
			args = append(args, dataset)
			err = sr.RunStream(ctx, nil, f, nil, "zfs", args...)
			if err == nil {
				created = true
			}
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return "", cleanup, err
		}
	}
	root := ""
	if m.Stream.Driver == "btrfs" {
		last := filepath.Join(received, m.ID)
		if _, err := e.R.Run(ctx, nil, nil, "btrfs", "property", "set", "-ts", last, "ro", "false"); err != nil {
			return "", cleanup, err
		}
		root = filepath.Join(last, m.Stream.RootRelative)
	} else {
		if _, err := e.R.Run(ctx, nil, nil, "zfs", "set", "readonly=off", dataset); err != nil {
			return "", cleanup, err
		}
		if _, err := e.R.Run(ctx, nil, nil, "zfs", "mount", dataset); err != nil {
			return "", cleanup, err
		}
		root = filepath.Join(received, m.Stream.RootRelative)
	}
	return root, cleanup, nil
}

// Normalize ACL identities and namespaced capability root IDs only in recovered private staging.
func unshiftXattr(name string, b []byte, ranges []idRange) ([]byte, bool, error) {
	out := append([]byte{}, b...)
	changed := false
	if name == "security.capability" {
		if len(out) == 24 {
			id := binary.LittleEndian.Uint32(out[20:24])
			if id != 0 {
				mapped, err := mappedID(id, true, ranges)
				if err != nil {
					return nil, false, err
				}
				binary.LittleEndian.PutUint32(out[20:24], mapped)
				changed = mapped != id
			}
		}
		return out, changed, nil
	}
	if len(out) < 4 || (len(out)-4)%8 != 0 || binary.LittleEndian.Uint32(out[:4]) != 2 {
		return nil, false, errors.New("unsupported POSIX ACL encoding")
	}
	for i := 4; i+8 <= len(out); i += 8 {
		tag := binary.LittleEndian.Uint16(out[i : i+2])
		if tag != 2 && tag != 8 {
			continue
		}
		id := binary.LittleEndian.Uint32(out[i+4 : i+8])
		mapped, err := mappedID(id, tag == 2, ranges)
		if err != nil {
			return nil, false, err
		}
		binary.LittleEndian.PutUint32(out[i+4:i+8], mapped)
		changed = changed || mapped != id
	}
	return out, changed, nil
}

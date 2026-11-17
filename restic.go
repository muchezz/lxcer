package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Repository struct {
	R         Runner
	D         Destination
	Retries   int
	Bandwidth int
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
		if !ok {
			return nil, errors.New("password environment variable missing")
		}
		env = append(env, "RESTIC_PASSWORD="+p, "RESTIC_PASSWORD_FILE=")
	case r.D.Password != "":
		env = append(env, "RESTIC_PASSWORD="+r.D.Password, "RESTIC_PASSWORD_FILE=")
	}
	return env, nil
}
func (r Repository) run(ctx context.Context, args ...string) ([]byte, error) {
	env, e := r.env()
	if e != nil {
		return nil, e
	}
	var b []byte
	for i := 0; i <= r.Retries; i++ {
		b, e = r.R.Run(ctx, env, nil, "restic", args...)
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
	return nil, fmt.Errorf("destination %s: operation %s failed", r.D.Name, args[0])
}
func (r Repository) backup(ctx context.Context, paths []string, tags []string) (string, error) {
	args := []string{"backup", "--json"}
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
		return "", e
	}
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 4096), 1<<20)
	id := ""
	for s.Scan() {
		var row struct {
			Type string `json:"message_type"`
			ID   string `json:"snapshot_id"`
		}
		if e = json.Unmarshal(s.Bytes(), &row); e != nil {
			return "", e
		}
		if row.Type == "summary" {
			id = row.ID
		}
	}
	if e = s.Err(); e != nil {
		return "", e
	}
	if !snapshotID(id) {
		return "", errors.New("restic returned no valid snapshot ID")
	}
	return id, nil
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
func (r Repository) verify(ctx context.Context, id string, paths []string) error {
	if !snapshotID(id) {
		return errors.New("invalid snapshot id")
	}
	b, e := r.run(ctx, "snapshots", "--json", id)
	if e != nil {
		return e
	}
	var snaps []struct {
		ID    string   `json:"id"`
		Paths []string `json:"paths"`
	}
	if e = json.Unmarshal(b, &snaps); e != nil {
		return e
	}
	if len(snaps) != 1 || !strings.HasPrefix(snaps[0].ID, id) {
		return errors.New("snapshot missing or ambiguous")
	}
	for _, p := range paths {
		found := false
		for _, q := range snaps[0].Paths {
			if p == q {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("snapshot missing expected path %s", p)
		}
		b, e = r.run(ctx, "ls", "--json", id, p)
		if e != nil {
			return e
		}
		s := bufio.NewScanner(bytes.NewReader(b))
		found = false
		for s.Scan() {
			var row struct {
				Type string `json:"struct_type"`
				Path string `json:"path"`
			}
			if json.Unmarshal(s.Bytes(), &row) == nil && row.Type == "node" && row.Path == p {
				found = true
			}
		}
		if e = s.Err(); e != nil {
			return e
		}
		if !found {
			return errors.New("snapshot tree does not contain expected path")
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
func (r Repository) retention(ctx context.Context, host, instance string, dry bool) error {
	if r.D.KeepLast == 0 && r.D.KeepDaily == 0 && r.D.KeepWeekly == 0 {
		return errors.New("retention not configured")
	}
	a := []string{"forget", "--tag", "lxcer-data,host=" + host + ",instance=" + instance, "--group-by", ""}
	for _, v := range []struct {
		flag string
		n    int
	}{{"--keep-last", r.D.KeepLast}, {"--keep-daily", r.D.KeepDaily}, {"--keep-weekly", r.D.KeepWeekly}} {
		if v.n > 0 {
			a = append(a, v.flag, strconv.Itoa(v.n))
		}
	}
	if dry {
		a = append(a, "--dry-run")
	}
	_, e := r.run(ctx, a...)
	return e
}

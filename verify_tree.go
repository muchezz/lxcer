package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

type nodeFinder struct {
	pending []byte
	path    string
	found   bool
}

func (w *nodeFinder) row(b []byte) error {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	var row struct {
		Type string `json:"struct_type"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(b, &row); err != nil {
		return err
	}
	if row.Type == "node" && row.Path == w.path {
		w.found = true
	}
	return nil
}
func (w *nodeFinder) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(w.pending)+len(p) > 1<<20 {
				return 0, errors.New("restic tree record exceeds limit")
			}
			w.pending = append(w.pending, p...)
			break
		}
		if len(w.pending)+i > 1<<20 {
			return 0, errors.New("restic tree record exceeds limit")
		}
		w.pending = append(w.pending, p[:i]...)
		if err := w.row(w.pending); err != nil {
			return 0, err
		}
		w.pending = w.pending[:0]
		p = p[i+1:]
	}
	return n, nil
}
func (r Repository) verifyNode(ctx context.Context, id, path string) error {
	finder := &nodeFinder{path: path}
	if sr, ok := r.R.(StreamingRunner); ok {
		env, err := r.env()
		if err != nil {
			return err
		}
		if err = sr.RunStream(ctx, env, nil, finder, "restic", "ls", "--json", id, path); err != nil {
			return err
		}
	} else {
		b, err := r.run(ctx, "ls", "--json", id, path)
		if err != nil {
			return err
		}
		if _, err = finder.Write(b); err != nil {
			return err
		}
	}
	if err := finder.row(finder.pending); err != nil {
		return err
	}
	if !finder.found {
		return errors.New("snapshot tree does not contain expected path")
	}
	return nil
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

func (l LXD) poolFree(ctx context.Context, c Container) (int64, error) {
	for _, a := range attachments(c) {
		if a.Target != "/" || a.Pool == "" {
			continue
		}
		if !validName(a.Pool) {
			return -1, errors.New("invalid source pool name")
		}
		b, err := l.run(ctx, "query", l.ref("/1.0/storage-pools/"+a.Pool+"/resources"))
		if err != nil {
			return -1, err
		}
		b, err = unwrapAPI(b)
		if err != nil {
			return -1, err
		}
		var data struct {
			Space struct {
				Total uint64 `json:"total"`
				Used  uint64 `json:"used"`
			} `json:"space"`
		}
		if err = json.Unmarshal(b, &data); err != nil {
			return -1, err
		}
		if data.Space.Total == 0 || data.Space.Used > data.Space.Total || data.Space.Total-data.Space.Used > 1<<63-1 {
			return -1, errors.New("pool capacity is unavailable")
		}
		return int64(data.Space.Total - data.Space.Used), nil
	}
	return -1, errors.New("legacy root pool cannot be identified")
}
func (e Engine) poolPreflight(ctx context.Context, l LXD, c Container, m *Manifest) error {
	m.PoolFreeBytes = -1
	if e.C.MinFreeBytes == 0 && !e.C.RequirePoolSpace {
		return nil
	}
	free, err := l.poolFree(ctx, c)
	if err != nil {
		m.PoolPreflightError = "source pool capacity NOT VERIFIED"
		if e.C.RequirePoolSpace {
			return fmt.Errorf("source capacity preflight required: %w", err)
		}
		e.Log.Warn("source pool capacity NOT VERIFIED", "host", m.Host, "instance", c.Name)
		return nil
	}
	m.PoolFreeBytes = free
	if uint64(free) < e.C.MinFreeBytes {
		return errors.New("source LXD storage pool has insufficient free space")
	}
	return nil
}

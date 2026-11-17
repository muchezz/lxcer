package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

type retainedSnapshot struct {
	Confirmed bool      `json:"-"`
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	Tags      []string  `json:"tags"`
}

// A chain is one retention unit. Never forget a parent while retaining a child.
func retentionIDs(snapshots []retainedSnapshot, d Destination, host, instance string) ([]string, error) {
	if d.KeepLast == 0 && d.KeepDaily == 0 && d.KeepWeekly == 0 {
		return nil, errors.New("retention not configured")
	}
	type group struct {
		key       string
		time      time.Time
		ids       []string
		protected bool
	}
	groups := map[string]*group{}
	for _, s := range snapshots {
		if !snapshotID(s.ID) {
			return nil, errors.New("repository returned invalid snapshot ID")
		}
		tags := map[string]bool{}
		chain := ""
		for _, tag := range s.Tags {
			tags[tag] = true
			if strings.HasPrefix(tag, "chain=") {
				chain = strings.TrimPrefix(tag, "chain=")
			}
		}
		if !tags["lxcer-data"] || !tags["host="+host] || !tags["instance="+instance] {
			return nil, errors.New("retention response escaped explicit scope")
		}
		key := s.ID
		if tags["lxcer-chain"] {
			if !validName(chain) {
				return nil, errors.New("chain snapshot lacks valid retention root")
			}
			key = chain
		}
		g := groups[key]
		if g == nil {
			g = &group{key: key}
			groups[key] = g
		}
		if tags["lxcer-chain"] && !s.Confirmed {
			g.protected = true
		}
		g.ids = append(g.ids, s.ID)
		if s.Time.After(g.time) {
			g.time = s.Time
		}
	}
	ordered := make([]*group, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].time.Equal(ordered[j].time) {
			return ordered[i].key < ordered[j].key
		}
		return ordered[i].time.After(ordered[j].time)
	})
	keep := map[string]bool{}
	daily := map[string]bool{}
	weekly := map[string]bool{}
	eligible := 0
	for _, g := range ordered {
		if g.protected {
			keep[g.key] = true
			continue
		}
		i := eligible
		eligible++
		if i == 0 || i < d.KeepLast {
			keep[g.key] = true
		}
		day := g.time.UTC().Format("2006-01-02")
		year, week := g.time.UTC().ISOWeek()
		wk := strconv.Itoa(year) + "-" + strconv.Itoa(week)
		if !daily[day] && len(daily) < d.KeepDaily {
			daily[day] = true
			keep[g.key] = true
		}
		if !weekly[wk] && len(weekly) < d.KeepWeekly {
			weekly[wk] = true
			keep[g.key] = true
		}
	}
	var forget []string
	for _, g := range ordered {
		if !keep[g.key] {
			forget = append(forget, g.ids...)
		}
	}
	sort.Strings(forget)
	return forget, nil
}
func (r Repository) chainRetention(ctx context.Context, host, instance string, dry bool) error {
	b, err := r.run(ctx, "snapshots", "--json", "--tag", "lxcer-data,host="+host+",instance="+instance)
	if err != nil {
		return err
	}
	var rows []retainedSnapshot
	if err = json.Unmarshal(b, &rows); err != nil {
		return err
	}

	receiptJSON, err := r.run(ctx, "snapshots", "--json", "--tag", "lxcer-receipt,host="+host+",instance="+instance)
	if err != nil {
		return err
	}
	var receipts []retainedSnapshot
	if err = json.Unmarshal(receiptJSON, &receipts); err != nil {
		return err
	}
	confirmed := map[string]bool{}
	for _, receipt := range receipts {
		for _, tag := range receipt.Tags {
			if strings.HasPrefix(tag, "job=") {
				confirmed[tag] = true
			}
		}
	}
	for i, row := range rows {
		for _, tag := range row.Tags {
			if confirmed[tag] {
				rows[i].Confirmed = true
			}
		}
	}
	ids, err := retentionIDs(rows, r.D, host, instance)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	args := []string{"forget"}
	if dry {
		args = append(args, "--dry-run")
	}
	args = append(args, ids...)
	_, err = r.run(ctx, args...)
	return err
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Container struct {
	ExpandedConfig  map[string]string            `json:"expanded_config"`
	Name            string                       `json:"name"`
	StatusCode      int                          `json:"status_code"`
	Type            string                       `json:"type"`
	Config          map[string]string            `json:"config"`
	Devices         map[string]map[string]string `json:"devices"`
	ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
	Profiles        []string                     `json:"profiles"`
	Architecture    string                       `json:"architecture"`
}
type Capabilities struct {
	Server           string   `json:"server"`
	Client           string   `json:"client"`
	Extensions       []string `json:"extensions"`
	Export           bool     `json:"export"`
	Refresh          bool     `json:"refresh"`
	InstanceEndpoint bool     `json:"instance_endpoint"`
}

func (c Capabilities) Has(s string) bool {
	for _, x := range c.Extensions {
		if x == s {
			return true
		}
	}
	return false
}

type LXD struct {
	R    Runner
	Host string
	Caps Capabilities
}

func (l LXD) ref(n string) string {
	if l.Host == "" || l.Host == "local" {
		return "local:" + n
	}
	return l.Host + ":" + n
}
func (l LXD) run(ctx context.Context, args ...string) ([]byte, error) {
	return l.R.Run(ctx, nil, nil, "lxc", args...)
}
func (l *LXD) Detect(ctx context.Context) error {
	b, e := l.run(ctx, "query", l.ref("/1.0"))
	if e != nil {
		return fmt.Errorf("capability discovery: %w; configure authenticated lxc remote", e)
	}
	var root struct {
		Metadata    json.RawMessage `json:"metadata"`
		Environment struct {
			ServerVersion string `json:"server_version"`
		} `json:"environment"`
		Extensions []string `json:"api_extensions"`
	}
	if e = json.Unmarshal(b, &root); e != nil {
		return e
	}
	if len(root.Metadata) > 0 {
		if e = json.Unmarshal(root.Metadata, &root); e != nil {
			return e
		}
	}
	l.Caps.Server = root.Environment.ServerVersion
	l.Caps.Extensions = root.Extensions
	if l.Caps.Server == "" {
		return errors.New("server returned no version")
	}
	b, e = l.run(ctx, "version")
	if e != nil {
		return e
	}
	l.Caps.Client = strings.TrimSpace(string(b))
	b, e = l.run(ctx, "export", "--help")
	l.Caps.Export = e == nil && strings.Contains(string(b), "export") && l.Caps.Has("container_backup")
	b, e = l.run(ctx, "copy", "--help")
	l.Caps.Refresh = e == nil && strings.Contains(string(b), "--refresh") && l.Caps.Has("container_incremental_copy")
	l.Caps.InstanceEndpoint = l.Caps.Has("instances")
	return nil
}
func (l LXD) List(ctx context.Context) ([]Container, error) {
	b, e := l.run(ctx, "list", l.ref(""), "--format", "json")
	if e != nil {
		return nil, e
	}
	var cc []Container
	e = json.Unmarshal(b, &cc)
	return cc, e
}
func filterContainers(cc []Container, blacklist []string) []Container {
	var out []Container
	for _, c := range cc {
		excluded := false
		for _, b := range blacklist {
			if b == c.Name {
				excluded = true
			}
		}
		if !excluded {
			out = append(out, c)
		}
	}
	return out
}
func selectContainers(cc []Container, blacklist []string, name string) ([]Container, error) {
	out := filterContainers(cc, blacklist)
	if name != "" {
		if !validName(name) {
			return nil, errors.New("invalid instance name")
		}
		for _, c := range out {
			if c.Name == name {
				return []Container{c}, nil
			}
		}
		return nil, fmt.Errorf("requested instance %q missing or excluded", name)
	}
	if len(out) == 0 {
		return nil, errors.New("no instances matched")
	}
	return out, nil
}

type Attachment struct {
	Device   string `json:"device"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Pool     string `json:"pool,omitempty"`
	Included bool   `json:"included"`
	Handling string `json:"handling"`
}

func attachments(c Container) []Attachment {
	var out []Attachment
	devices := c.ExpandedDevices
	if devices == nil {
		devices = c.Devices
	}
	for n, d := range devices {
		if d["type"] == "disk" {
			root := d["path"] == "/"
			out = append(out, Attachment{Device: n, Source: d["source"], Target: d["path"], Pool: d["pool"], Included: root, Handling: map[bool]string{true: "root filesystem", false: "separate backup required"}[root]})
		}
	}
	return out
}
func (l LXD) Storage(ctx context.Context, c Container) (string, error) {
	for _, a := range attachments(c) {
		if a.Target == "/" && a.Pool != "" {
			b, e := l.run(ctx, "query", l.ref("/1.0/storage-pools/"+a.Pool))
			if e != nil {
				return "", e
			}
			var pool struct {
				Driver   string `json:"driver"`
				Metadata struct {
					Driver string `json:"driver"`
				} `json:"metadata"`
			}
			if e = json.Unmarshal(b, &pool); e != nil {
				return "", e
			}
			if pool.Driver != "" {
				return pool.Driver, nil
			}
			if pool.Metadata.Driver != "" {
				return pool.Metadata.Driver, nil
			}
			return "", errors.New("pool driver missing")
		}
	}
	return "legacy-unknown", nil
}
func chooseStrategy(p Policy, cap Capabilities, c Container) (string, error) {
	s := p.Strategy
	if s == "" || s == "auto" {
		if cap.Export {
			s = "full"
		} else {
			s = "snapshot"
		}
	}
	if c.Type == "virtual-machine" && s == "snapshot" {
		return "", errors.New("legacy image strategy supports containers only")
	}
	switch s {
	case "full":
		if !cap.Export {
			return "", errors.New("full export unavailable: use snapshot strategy with legacy metadata")
		}
	case "snapshot":
	case "incremental":
		if !cap.Refresh || p.Mirror == "" {
			return "", errors.New("incremental requires refresh capability and mirror remote")
		}
	case "native":
		if p.NativeRoot == "" {
			return "", errors.New("native requires native_root: the source root filesystem directory")
		}
		if len(p.Paths) == 0 {
			return "", errors.New("native requires explicit filesystem paths")
		}
		if c.StatusCode == 103 && len(p.Hooks.Quiesce) == 0 {
			return "", errors.New("running native backup requires quiesce/resume hooks")
		}
	default:
		return "", errors.New("unknown strategy")
	}
	return s, nil
}

func effectiveConfig(c Container) map[string]string {
	cfg := map[string]string{}
	for k, v := range c.ExpandedConfig {
		cfg[k] = v
	}
	for k, v := range c.Config {
		cfg[k] = v
	}
	return cfg
}

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func validName(s string) bool { return namePattern.MatchString(s) && s != "." && s != ".." }

type Hook struct {
	Argv     []string      `yaml:"argv"`
	Timeout  time.Duration `yaml:"timeout"`
	Optional bool          `yaml:"optional"`
}
type Hooks struct {
	Pre     []Hook `yaml:"pre"`
	Quiesce []Hook `yaml:"quiesce"`
	Resume  []Hook `yaml:"resume"`
	Post    []Hook `yaml:"post"`
}
type Policy struct {
	BackupVolumes bool     `yaml:"backup_volumes"`
	NativeRoot    string   `yaml:"native_root"`
	Strategy      string   `yaml:"strategy"`
	Hooks         Hooks    `yaml:"hooks"`
	Paths         []string `yaml:"paths"`
	AllowExcluded bool     `yaml:"allow_excluded"`
	Mirror        string   `yaml:"mirror"`
}
type Host struct {
	Name         string            `yaml:"name"`
	Workers      int               `yaml:"workers"`
	Policy       Policy            `yaml:"policy"`
	Instances    map[string]Policy `yaml:"instances"`
	SSH          string            `yaml:"ssh"`
	RemoteConfig string            `yaml:"remote_config"`
}
type Destination struct {
	Name         string `yaml:"name"`
	Path         string `yaml:"path"`
	Password     string `yaml:"password" json:"-"`
	PasswordFile string `yaml:"password_file" json:"-"`
	PasswordEnv  string `yaml:"password_env" json:"-"`
	Optional     bool   `yaml:"optional"`
	Check        bool   `yaml:"check"`
	ReadData     bool   `yaml:"read_data"`
	KeepLast     int    `yaml:"keep_last"`
	KeepDaily    int    `yaml:"keep_daily"`
	KeepWeekly   int    `yaml:"keep_weekly"`
}
type Config struct {
	Legacy            bool          `yaml:"-"`
	Version           int           `yaml:"version"`
	Hosts             []string      `yaml:"hosts"`
	Inventory         []Host        `yaml:"inventory"`
	Blacklist         []string      `yaml:"blacklist"`
	Destinations      []Destination `yaml:"destinations"`
	BackupResticRepos []Destination `yaml:"backup_restic_repos"`
	RestoreResticRepo Destination   `yaml:"restore_restic_repo"`
	LocalWorkers      int           `yaml:"local_workers"`
	Workers           int           `yaml:"workers"`
	StateDir          string        `yaml:"state_dir"`
	Timeout           time.Duration `yaml:"timeout"`
	Retries           int           `yaml:"retries"`
	Bandwidth         int           `yaml:"bandwidth_kib"`
	Compression       string        `yaml:"compression"`
	MinFreeBytes      uint64        `yaml:"min_free_bytes"`
	Policy            Policy        `yaml:"policy"`
	MetricsFile       string        `yaml:"metrics_file"`
}

func readConfig(path string) (Config, error) {
	c := Config{Version: 1, Workers: 1, StateDir: "./lxcer-state", Timeout: time.Hour, Compression: "none", MinFreeBytes: 1 << 30}
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	var fields map[string]yaml.Node
	if e = yaml.Unmarshal(b, &fields); e != nil {
		return c, e
	}
	_, versioned := fields["version"]
	c.Legacy = !versioned
	if c.Legacy {
		if _, set := fields["compression"]; !set {
			c.Compression = "zstd"
		}
	}
	if len(c.Destinations) == 0 {
		c.Destinations = c.BackupResticRepos
	}
	if c.LocalWorkers > 0 {
		c.Workers = c.LocalWorkers
	}
	if v := os.Getenv("LXCER_STATE_DIR"); v != "" {
		c.StateDir = v
	}
	if v := os.Getenv("LXCER_PASSWORD_FILE"); v != "" {
		for i := range c.Destinations {
			c.Destinations[i].PasswordFile = v
			c.Destinations[i].Password = ""
			c.Destinations[i].PasswordEnv = ""
		}
		if c.RestoreResticRepo.Path != "" {
			c.RestoreResticRepo.PasswordFile = v
			c.RestoreResticRepo.Password = ""
			c.RestoreResticRepo.PasswordEnv = ""
		}
	}
	for i := range c.Destinations {
		if c.Destinations[i].Name == "" {
			c.Destinations[i].Name = fmt.Sprintf("repo-%d", i+1)
		}
	}
	if len(c.Inventory) == 0 {
		for _, h := range c.Hosts {
			c.Inventory = append(c.Inventory, Host{Name: h, Workers: 1})
		}
	}
	if c.Policy.Strategy == "" {
		c.Policy.Strategy = "auto"
		if c.Legacy {
			c.Policy.Strategy = "snapshot"
		}
	}
	c.StateDir, e = filepath.Abs(c.StateDir)
	if e != nil {
		return c, e
	}
	return c, c.Validate()
}
func validatePolicy(p Policy) error {
	switch p.Strategy {
	case "", "auto", "full", "snapshot", "native", "incremental":
	default:
		return fmt.Errorf("unknown strategy %q", p.Strategy)
	}
	if p.Mirror != "" && !validName(p.Mirror) {
		return errors.New("mirror must be an authenticated LXD remote name")
	}
	if p.NativeRoot != "" && (!filepath.IsAbs(p.NativeRoot) || filepath.Clean(p.NativeRoot) == "/") {
		return errors.New("native_root must be an explicit absolute directory below /")
	}
	for _, path := range p.Paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
			return errors.New("native paths must be absolute and cannot be /")
		}
	}
	for _, hs := range [][]Hook{p.Hooks.Pre, p.Hooks.Quiesce, p.Hooks.Resume, p.Hooks.Post} {
		for _, h := range hs {
			if len(h.Argv) == 0 || h.Argv[0] == "" || h.Timeout < 0 {
				return errors.New("invalid hook")
			}
		}
	}
	if len(p.Hooks.Quiesce) > 0 && len(p.Hooks.Resume) == 0 {
		return errors.New("quiesce requires resume hooks")
	}
	return nil
}
func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("unsupported configuration version")
	}
	if c.Workers < 1 || c.Workers > 256 || c.Timeout <= 0 || c.Retries < 0 || c.Retries > 10 || c.Bandwidth < 0 {
		return errors.New("invalid concurrency, timeout, retry or bandwidth setting")
	}
	if c.Compression != "none" && c.Compression != "zstd" {
		return errors.New("compression must be none or zstd")
	}
	if e := validatePolicy(c.Policy); e != nil {
		return e
	}
	names := map[string]bool{}
	for _, h := range c.Inventory {
		if !validName(h.Name) || names[h.Name] || h.Workers < 0 || h.Workers > 256 {
			return errors.New("invalid or duplicate inventory host")
		}
		names[h.Name] = true
		if e := validatePolicy(h.Policy); e != nil {
			return e
		}
		for n, p := range h.Instances {
			if !validName(n) {
				return errors.New("invalid instance policy name")
			}
			if e := validatePolicy(p); e != nil {
				return e
			}
		}
		if h.SSH != "" && (!validName(h.SSH) || !filepath.IsAbs(h.RemoteConfig)) {
			return errors.New("SSH requires a safe ssh-config alias and absolute remote_config")
		}
	}
	names = map[string]bool{}
	validationDestinations := append([]Destination{}, c.Destinations...)
	if c.RestoreResticRepo.Path != "" {
		d := c.RestoreResticRepo
		d.Name = "legacy-restore"
		validationDestinations = append(validationDestinations, d)
	}
	for _, d := range validationDestinations {
		if !validName(d.Name) || names[d.Name] || d.Path == "" || d.KeepLast < 0 || d.KeepDaily < 0 || d.KeepWeekly < 0 {
			return errors.New("invalid or duplicate destination")
		}
		names[d.Name] = true
		n := 0
		for _, v := range []string{d.Password, d.PasswordFile, d.PasswordEnv} {
			if v != "" {
				n++
			}
		}
		if n > 1 {
			return errors.New("choose one password source per destination")
		}
		if d.PasswordFile != "" {
			s, e := os.Stat(d.PasswordFile)
			if e != nil {
				return e
			}
			if !s.Mode().IsRegular() || s.Mode().Perm()&0077 != 0 {
				return errors.New("password file must be regular with mode 0600 or stricter")
			}
		}
	}
	return nil
}
func mergePolicy(base, p Policy) Policy {
	if p.BackupVolumes {
		base.BackupVolumes = true
	}
	if p.NativeRoot != "" {
		base.NativeRoot = p.NativeRoot
	}
	if p.Strategy != "" {
		base.Strategy = p.Strategy
	}
	if p.Paths != nil {
		base.Paths = p.Paths
	}
	if p.Mirror != "" {
		base.Mirror = p.Mirror
	}
	if p.AllowExcluded {
		base.AllowExcluded = true
	}
	if p.Hooks.Pre != nil {
		base.Hooks.Pre = p.Hooks.Pre
	}
	if p.Hooks.Post != nil {
		base.Hooks.Post = p.Hooks.Post
	}
	if p.Hooks.Quiesce != nil {
		base.Hooks.Quiesce = p.Hooks.Quiesce
	}
	if p.Hooks.Resume != nil {
		base.Hooks.Resume = p.Hooks.Resume
	}
	return base
}

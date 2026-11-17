package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

var version = "dev"
var commit = "unknown"
var buildDate = "unknown"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, errOut io.Writer) int {
	return runWithRunner(args, out, errOut, ExecRunner{})
}
func runWithRunner(args []string, out, errOut io.Writer, runner Runner) int {
	legacyFlags := len(args) > 0 && strings.HasPrefix(args[0], "-")
	f := flag.NewFlagSet("lxcer", flag.ContinueOnError)
	f.SetOutput(errOut)
	sourceHost := f.String("source-host", "", "catalog source host filter")
	legacy := f.Bool("legacy", false, "include pre-modernization repository snapshots in discovery")
	legacyMetadata := f.String("legacy-metadata", "", "JSON instance metadata for adopting an old archive")
	artifactPath := f.String("artifact-path", "", "original absolute archive path in a legacy snapshot")
	action := f.String("a", "", "backup, list, discover, verify, restore-plan, restore, restore-test, cleanup, recover, capabilities, validate, retention, recover-catalog, adopt-legacy, version")
	config := f.String("config", "conf.yml", "YAML configuration")
	stopped := f.Bool("stopped", false, "keep a legacy restore stopped instead of starting it")
	local := f.Bool("local", false, "use local LXD")
	host := f.String("remote-host", "", "authenticated LXD remote")
	name := f.String("container", "", "select exactly one instance")
	as := f.String("as", "", "restore name")
	concurrent := f.Bool("concurrently", false, "use configured concurrency")
	restoreList := f.String("restore-list", "", "strict source:target restore list")
	cleanup := f.Bool("cleanup", false, "obsolete: use cleanup command")
	manifest := f.String("manifest", "", "job manifest path")
	dest := f.String("destination", "", "destination name")
	snapshot := f.String("snapshot", "", "exact Restic snapshot ID")
	execute := f.Bool("execute", false, "execute cleanup, recovery, or configured scoped retention")
	pool := f.String("storage-pool", "", "restore pool mapping")
	networks := f.String("network-map", "", "comma-separated old=new network mappings")
	isolated := f.Bool("isolated-target", false, "acknowledge dedicated isolated target host")
	profile := f.String("profile", "", "root-only isolated restore profile")
	health := f.String("health-argv", "", "optional JSON argv health check inside isolated test instance")
	target := f.String("target", "", "new catalog recovery directory")
	restoreVolumes := f.Bool("restore-volumes", false, "restore exported managed volumes under new names")
	nativeTarget := f.String("native-target", "", "new filesystem restore directory")
	defaultLevel := "info"
	if legacyFlags {
		defaultLevel = "error"
	}
	level := f.String("log-level", defaultLevel, "debug, info, warn, error")
	jsonLog := f.Bool("json-log", !legacyFlags, "structured JSON logging")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append([]string{"-a", args[0]}, args[1:]...)
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintln(errOut, "unexpected positional arguments")
		return 2
	}
	if *action == "version" {
		fmt.Fprintf(out, "lxcer %s commit=%s built=%s\n", version, commit, buildDate)
		return 0
	}
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(*level)); err != nil {
		fmt.Fprintln(errOut, "invalid log level")
		return 2
	}
	opts := &slog.HandlerOptions{Level: lv}
	var handler slog.Handler
	if *jsonLog {
		handler = slog.NewJSONHandler(errOut, opts)
	} else {
		handler = slog.NewTextHandler(errOut, opts)
	}
	log := slog.New(handler)
	fail := func(err error) int { log.Error("command failed", "operation", *action, "error", err); return 1 }
	if *cleanup {
		return fail(errors.New("-cleanup removed because it deleted unrelated resources; use cleanup preview, then -execute"))
	}
	c, err := readConfig(*config)
	if err != nil {
		return fail(err)
	}
	if *action == "validate" {
		fmt.Fprintln(out, "configuration valid")
		return 0
	}
	if !*concurrent {
		c.Workers = 1
	}
	if *host != "" && !validName(*host) {
		return fail(errors.New("invalid remote name"))
	}
	if *name != "" && !validName(*name) {
		return fail(errors.New("invalid container name"))
	}
	hosts := c.Inventory
	selectedHost := *host
	if *local {
		if *host != "" {
			return fail(errors.New("choose -local or -remote-host"))
		}
		selectedHost = "local"
	}
	if selectedHost != "" {
		h := Host{Name: selectedHost, Workers: 1}
		for _, x := range hosts {
			if x.Name == selectedHost {
				h = x
			}
		}
		hosts = []Host{h}
	}
	e := Engine{C: c, R: runner, Log: log}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, c.Timeout)
	defer stop()
	var m Manifest
	if *manifest != "" {
		m, err = loadManifest(*manifest)
		if err != nil {
			return fail(err)
		}
	}
	switch *action {
	case "backup":
		if len(hosts) == 0 {
			return fail(errors.New("no hosts selected; use -local or inventory"))
		}
		err = e.Backup(ctx, hosts, *name)
	case "capabilities":
		for _, h := range hosts {
			l := LXD{R: e.R, Host: h.Name}
			if err = l.Detect(ctx); err != nil {
				break
			}
			b, _ := json.Marshal(map[string]any{"host": h.Name, "capabilities": l.Caps})
			fmt.Fprintln(out, string(b))
		}
		if len(hosts) == 0 {
			err = errors.New("select a host")
		}
	case "list":
		var files []string
		files, err = filepath.Glob(filepath.Join(c.StateDir, "lxcer-*", "manifest.json"))
		if err == nil {
			for _, path := range files {
				b, er := os.ReadFile(path)
				if er != nil {
					err = er
					break
				}
				fmt.Fprintln(out, string(b))
			}
		}
	case "discover":
		var d Destination
		d, err = e.destination(*dest)
		if err == nil {
			var b []byte
			args := []string{"snapshots", "--json"}
			if !*legacy {
				args = append(args, "--tag", "lxcer-data")
			}
			b, err = e.repo(d).run(ctx, args...)
			if err == nil {
				fmt.Fprintln(out, string(b))
			}
		}
	case "adopt-legacy":
		var adopted Manifest
		adopted, err = e.adoptLegacy(ctx, *dest, *snapshot, *artifactPath, *legacyMetadata, *sourceHost)
		if err == nil {
			fmt.Fprintln(out, filepath.Join(c.StateDir, adopted.ID, "manifest.json"))
		}
	case "recover-catalog":
		var d Destination
		d, err = e.destination(*dest)
		if err == nil {
			err = e.recoverCatalog(ctx, d, *snapshot, *target)
		}
	case "verify":
		if *manifest == "" {
			err = errors.New("-manifest required")
			break
		}
		matched := 0
		for _, s := range m.Destinations {
			if *dest != "" && s.Name != *dest {
				continue
			}
			matched++
			d, er := e.destination(s.Name)
			if er == nil {
				paths := m.Paths
				er = e.repo(d).verify(ctx, s.SnapshotID, paths)
			}
			err = errors.Join(err, er)
		}
		if matched == 0 {
			err = errors.New("no destination matched verification request")
		}
	case "restore", "restore-plan", "restore-test":
		if selectedHost == "" {
			err = errors.New("explicit restore target required")
			break
		}

		ns := map[string]string{}
		if *networks != "" {
			for _, pair := range strings.Split(*networks, ",") {
				a, b, ok := strings.Cut(pair, "=")
				if !ok || !validName(a) || !validName(b) {
					err = errors.New("invalid network mapping")
					break
				}
				ns[a] = b
			}
		}
		if err != nil {
			break
		}
		var argv []string
		if *health != "" {
			if err = json.Unmarshal([]byte(*health), &argv); err != nil {
				break
			}
		}
		restoreDestination := *dest
		if restoreDestination == "" && c.RestoreResticRepo.Path != "" {
			restoreDestination = "legacy-restore"
			for i, d := range c.Destinations {
				if d.Path == c.RestoreResticRepo.Path {
					restoreDestination = d.Name
					override := c.RestoreResticRepo
					override.Name = d.Name
					e.C.Destinations[i] = override
					break
				}
			}
		}
		legacyRestore := legacyFlags && c.Legacy
		o := RestoreOptions{Start: legacyRestore && !*stopped, RestoreVolumes: *restoreVolumes, Name: *as, Host: selectedHost, Destination: restoreDestination, Snapshot: *snapshot, Pool: *pool, Networks: ns, DryRun: *action == "restore-plan", Test: *action == "restore-test", Isolated: *isolated, Profile: *profile, Health: argv, NativeTarget: *nativeTarget}

		pairs := map[string]string{}
		if *restoreList != "" {
			pairs, err = loadRestoreList(*restoreList)
			if err != nil {
				break
			}
		} else {
			source := *name
			if source == "" {
				source = m.Instance.Name
			}
			if source == "" {
				err = errors.New("restore requires -manifest, -container or -restore-list")
				break
			}
			pairs[source] = *as
		}

		var restoreErrors []error
		var restoreMu sync.Mutex
		var restoreWG sync.WaitGroup
		slots := make(chan struct{}, c.Workers)
		restoreSingle := func(source, to string) error {
			jobPath := *manifest
			job := m
			if jobPath == "" {
				var lookupErr error
				job, jobPath, lookupErr = e.findManifest(source, *sourceHost)
				if lookupErr != nil {
					if legacyRestore && *action == "restore" && *sourceHost == "" && errors.Is(lookupErr, errNoCatalog) {
						if *pool != "" || *networks != "" || *restoreVolumes {
							return errors.New("legacy archive mappings require explicit metadata adoption")
						}
						if er := e.restoreLegacy(ctx, source, to, selectedHost, restoreDestination, *snapshot, !*stopped); er != nil {
							return er
						}
						restoreMu.Lock()
						fmt.Fprintf(out, "legacy restore: %s -> %s:%s\n", source, selectedHost, to)
						restoreMu.Unlock()
						return nil
					}
					return lookupErr
				}
			}
			if source != job.Instance.Name {
				return errors.New("restore source does not match manifest")
			}
			options := o
			options.Name = to
			er := e.restore(ctx, &job, options)
			if *action == "restore-test" && job.RestoreTest != nil {
				er = errors.Join(er, job.save(filepath.Dir(jobPath)))
			}
			if er != nil {
				return er
			}
			state := "stopped"
			if options.Start {
				state = "started"
			}
			restoreMu.Lock()
			fmt.Fprintf(out, "%s: %s -> %s:%s (%s)\n", *action, source, selectedHost, to, state)
			restoreMu.Unlock()
			return nil
		}
		for source, to := range pairs {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				restoreMu.Lock()
				restoreErrors = append(restoreErrors, ctx.Err())
				restoreMu.Unlock()
				continue
			}
			restoreWG.Add(1)
			go func(source, to string) {
				defer restoreWG.Done()
				defer func() { <-slots }()
				if er := restoreSingle(source, to); er != nil {
					restoreMu.Lock()
					restoreErrors = append(restoreErrors, er)
					restoreMu.Unlock()
				}
			}(source, to)
		}
		restoreWG.Wait()

		err = errors.Join(restoreErrors...)
		if *action == "restore-test" {
			err = errors.Join(err, e.writeMetrics())
		}

	case "cleanup", "recover":
		err = e.recoverJobs(ctx, *execute)
		if *execute {
			err = errors.Join(err, e.writeMetrics())
		}
	case "retention":
		var d Destination
		d, err = e.destination(*dest)
		if err == nil {
			if selectedHost == "" || *name == "" {
				err = errors.New("retention requires explicit host and container scope")
			} else {
				err = e.repo(d).retention(ctx, selectedHost, *name, !*execute)
			}
		}
	default:
		err = errors.New("choose a supported action with -a; see -help")
	}
	if err != nil {
		return fail(err)
	}
	return 0
}
func loadRestoreList(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		a, b, ok := strings.Cut(strings.TrimSpace(s.Text()), ":")
		if !ok || !validName(a) || !validName(b) || m[a] != "" {
			return nil, errors.New("restore list requires unique source:target lines")
		}
		m[a] = b
	}
	if err = s.Err(); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, errors.New("empty restore list")
	}
	return m, nil
}

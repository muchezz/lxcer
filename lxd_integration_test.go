//go:build lxdintegration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Requires a disposable, already configured server. Never discovers or uses the default remote.
func TestIsolatedLXD(t *testing.T) {
	remote := os.Getenv("LXCER_TEST_REMOTE")
	profile := os.Getenv("LXCER_TEST_PROFILE")
	image := os.Getenv("LXCER_TEST_IMAGE")
	if os.Getenv("LXCER_TEST_DISPOSABLE") != "YES" {
		t.Skip("NOT VERIFIED: disposable integration was not enabled")
	}
	if !strings.HasPrefix(remote, "lxcer-test-") || !validName(remote) || !validName(profile) || image == "" {
		t.Fatal("requires explicitly disposable lxcer-test-* remote, root-only profile, and test image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	r := ExecRunner{}
	l := LXD{R: r, Host: remote}
	if er := l.Detect(ctx); er != nil {
		t.Fatal(er)
	}
	if expected := os.Getenv("LXCER_TEST_EXPECT_VERSION"); expected != "" && l.Caps.Server != expected && !strings.HasPrefix(l.Caps.Server, expected+".") {
		t.Fatalf("NOT VERIFIED: expected server %s, got %s", expected, l.Caps.Server)
	}
	if er := requireRootProfile(ctx, l, profile); er != nil {
		t.Fatal(er)
	}
	f := fake()
	e := testEngine(t, f)
	e.R = r
	e.C.Timeout = 20 * time.Minute
	e.C.Destinations[0].Path = filepath.Join(t.TempDir(), "repository")
	e.C.Destinations[0].Password = "disposable-integration-only"
	if _, er := e.repo(e.C.Destinations[0]).run(ctx, "init"); er != nil {
		t.Fatal(er)
	}
	for _, running := range []bool{false, true} {
		name := newID()
		_, er := l.run(ctx, "init", image, l.ref(name), "-p", profile, "-c", "boot.autostart=false", "-c", "user.lxcer.integration="+name)
		if er != nil {
			t.Fatal(er)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cc, err := l.List(cleanup)
			if err != nil {
				t.Error(err)
				return
			}
			for _, c := range cc {
				if c.Name == name && c.Config["user.lxcer.integration"] == name {
					if _, err = l.run(cleanup, "delete", l.ref(name), "--force"); err != nil {
						t.Error(err)
					}
				}
			}
		})
		if running {
			if _, er = l.run(ctx, "start", l.ref(name)); er != nil {
				t.Fatal(er)
			}
			if _, er = l.run(ctx, "exec", l.ref(name), "--", "touch", "/root/lxcer-verification"); er != nil {
				t.Fatal(er)
			}
		}
		cc, er := l.List(ctx)
		if er != nil {
			t.Fatal(er)
		}
		for _, c := range cc {
			if c.Name == name {
				driver, err := l.Storage(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				if expected := os.Getenv("LXCER_TEST_EXPECT_DRIVER"); expected != "" && driver != expected {
					t.Fatalf("NOT VERIFIED: expected driver %s got %s", expected, driver)
				}
			}
		}
		if er = e.Backup(ctx, []Host{{Name: remote}}, name); er != nil {
			t.Fatal(er)
		}
		var m Manifest
		for _, candidate := range manifests(t, e) {
			if candidate.Instance.Name == name {
				m = candidate
			}
		}
		restored := newID()
		o := RestoreOptions{Name: restored, Host: remote, Destination: "primary", Pool: os.Getenv("LXCER_TEST_POOL")}
		if er = e.restore(ctx, &m, o); er != nil {
			t.Fatal(er)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cc, err := l.List(cleanup)
			if err != nil {
				t.Error(err)
				return
			}
			for _, c := range cc {
				if c.Name == restored && c.Config["user.lxcer.restore"] != "" {
					if _, err = l.run(cleanup, "delete", l.ref(restored)); err != nil {
						t.Error(err)
					}
				}
			}
		})
		if running {
			if _, er = l.run(ctx, "file", "pull", l.ref(restored)+"/root/lxcer-verification", filepath.Join(t.TempDir(), "sentinel")); er != nil {
				t.Fatal(er)
			}
		}
		t.Logf("verified server=%s client=%s storage=%s strategy=%s running=%v", l.Caps.Server, l.Caps.Client, m.Storage, m.Strategy, running)
	}
}

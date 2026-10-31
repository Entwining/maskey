//go:build darwin && arm64

package agent

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/Entwining/maskey/internal/store"
)

func TestInstallCanonicalJob(t *testing.T) {
	home := t.TempDir()
	executable := filepath.Join(home, "Maskey <&>")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "mk")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	var calls [][]string
	control := func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		if args[0] == "print" {
			return errJobNotLoaded
		}
		return nil
	}
	ready := false
	if err := install(context.Background(), link, path, control, func(context.Context) error { ready = true; return nil }); err != nil {
		t.Fatal(err)
	}
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	want := [][]string{{"print", domain + "/" + serviceLabel}, {"bootstrap", domain, path}}
	if !reflect.DeepEqual(calls, want) || !ready {
		t.Fatalf("install calls=%v ready=%v", calls, ready)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plist struct {
		Args []string `xml:"dict>array>string"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plist.Args, []string{canonical, "--keychain-service"}) {
		t.Fatalf("program arguments=%v", plist.Args)
	}
	for _, property := range []string{
		"<key>PATH</key><string>/usr/bin:/bin</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<key>AssociatedBundleIdentifiers</key><string>io.github.entwining.maskey</string>",
		"<key>ProcessType</key><string>Background</string>",
		"<key>ExitTimeOut</key><integer>35</integer>",
	} {
		if !bytes.Contains(data, []byte(property)) {
			t.Errorf("missing launchd property %s", property)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("plist permissions: %v %v", info, err)
	}
	cmd := exec.Command("/usr/bin/plutil", "-lint", "--", path)
	cmd.Env = []string{"PATH=" + servicePATH}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid launchd plist: %v: %s", err, output)
	}
}

func TestInstallUpgradeAndFailures(t *testing.T) {
	for _, failure := range []string{"", "print", "bootout", "bootstrap", "ready"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "Maskey")
			if err := os.WriteFile(executable, []byte("unused"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, serviceLabel+".plist")
			old := bytes.Replace(jobPlist("/previous/Maskey"), []byte("</dict>\n</plist>"), []byte("<key>Nice</key><integer>5</integer>\n</dict>\n</plist>"), 1)
			if err := os.WriteFile(path, old, 0644); err != nil {
				t.Fatal(err)
			}
			injected := &store.Error{Kind: store.KindPrerequisite, Message: "synthetic host check failure"}
			var calls []string
			control := func(_ context.Context, args ...string) error {
				calls = append(calls, args[0])
				if args[0] == failure {
					return injected
				}
				return nil
			}
			err := install(context.Background(), executable, path, control, func(context.Context) error {
				calls = append(calls, "ready")
				if failure == "ready" {
					return injected
				}
				return nil
			})
			want := []string{"print", "bootout", "bootstrap", "ready"}
			if failure != "" {
				for i, step := range want {
					if step == failure {
						want = want[:i+1]
						break
					}
				}
				if err == nil {
					t.Fatal("failed installation reported success")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if failure == "ready" && !errors.Is(err, injected) {
				t.Fatalf("host check failure was hidden: %v", err)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
			backups, err := filepath.Glob(filepath.Join(dir, "."+serviceLabel+".backup-*"))
			if err != nil {
				t.Fatal(err)
			}
			if failure == "print" {
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(data, old) || len(backups) != 0 {
					t.Fatal("failed status query changed the existing configuration")
				}
				return
			}
			if len(backups) != 1 {
				t.Fatalf("configuration backups=%v", backups)
			}
			data, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(data, old) {
				t.Fatal("previous configuration was not preserved")
			}
		})
	}
}

// Reinstalling the same configuration has nothing to preserve, so repeated
// setups leave no backups behind.
func TestReinstallKeepsNoIdenticalBackup(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "Maskey")
	if err := os.WriteFile(executable, []byte("unused"), 0700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, serviceLabel+".plist")
	if err := os.WriteFile(path, jobPlist(resolved), 0600); err != nil {
		t.Fatal(err)
	}
	control := func(context.Context, ...string) error { return nil }
	ready := func(context.Context) error { return nil }
	for range 2 {
		if err := install(context.Background(), executable, path, control, ready); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := filepath.Glob(filepath.Join(dir, "."+serviceLabel+".backup-*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("configuration backups=%v %v", backups, err)
	}
}

func TestInstallRejectsForeignConfiguration(t *testing.T) {
	for _, kind := range []string{"label", "arguments", "symlink", "registered-only"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "Maskey")
			if err := os.WriteFile(executable, []byte("unused"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, serviceLabel+".plist")
			data := jobPlist(executable)
			switch kind {
			case "label":
				data = bytes.Replace(data, []byte(serviceLabel), []byte("another.job"), 1)
			case "arguments":
				data = bytes.Replace(data, []byte("--keychain-service"), []byte("--another-service"), 1)
			}
			if kind == "symlink" {
				if err := os.Symlink(executable, path); err != nil {
					t.Fatal(err)
				}
			} else if kind != "registered-only" {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var calls []string
			err := install(context.Background(), executable, path, func(_ context.Context, args ...string) error {
				calls = append(calls, args[0])
				return nil
			}, func(context.Context) error { t.Fatal("foreign job reached readiness"); return nil })
			if !errors.Is(err, errServiceCollision) {
				t.Fatalf("foreign job: %v", err)
			}
			wantCalls := 0
			if kind == "registered-only" {
				wantCalls = 1
			}
			if len(calls) != wantCalls {
				t.Fatalf("foreign job reached launchctl: %v", calls)
			}
			if kind != "symlink" && kind != "registered-only" {
				current, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(current, data) {
					t.Fatal("foreign configuration was replaced")
				}
			}
		})
	}
}

package activity

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The ancestry walk over a process table: it stops before launchd and at a
// reused pid, keeps at most maxAncestors, and names a process whose path the
// kernel withholds.
func TestInvokerLineage(t *testing.T) {
	table := map[int]process{
		1:  {parent: 0, start: 0, path: "/sbin/launchd", name: "launchd"},
		10: {parent: 1, start: 10, path: "/Applications/Terminal.app/Contents/MacOS/Terminal", name: "Terminal"},
		11: {parent: 10, start: 11, name: "login"},
		12: {parent: 11, start: 12, path: "/bin/zsh", name: "zsh"},
		13: {parent: 12, start: 13, path: "/usr/local/bin/claude", name: "claude"},
		14: {parent: 13, start: 14, path: "/Applications/Maskey.app/Contents/MacOS/Maskey", name: "Maskey"},
		// 20 started after 21: it took the pid of 21's parent after that exited.
		20: {parent: 1, start: 30, path: "/usr/bin/newer", name: "newer"},
		21: {parent: 20, start: 21, path: "/bin/sh", name: "sh"},
		22: {parent: 21, start: 22, path: "/usr/bin/make", name: "make"},
		23: {parent: 22, start: 23, path: "/usr/bin/mk", name: "mk"},
		// 98 has exited.
		99: {parent: 98, start: 99, path: "/usr/bin/mk", name: "mk"},
	}
	for pid := 100; pid <= 112; pid++ {
		table[pid] = process{parent: max(pid-1, 1), start: int64(pid), path: fmt.Sprintf("/bin/p%d", pid)}
	}
	lookup := func(pid int) (process, bool) {
		p, ok := table[pid]
		return p, ok
	}
	for _, test := range []struct {
		name  string
		pid   int
		exe   string
		chain []string
	}{
		{"stops before launchd", 14, "/usr/local/bin/claude", []string{"/bin/zsh", "login", "/Applications/Terminal.app/Contents/MacOS/Terminal"}},
		{"stops at a reused pid", 23, "/usr/bin/make", []string{"/bin/sh"}},
		{"parent's pid reused", 21, "", []string{}},
		{"keeps the nearest ancestors", 112, "/bin/p111", []string{"/bin/p110", "/bin/p109", "/bin/p108", "/bin/p107", "/bin/p106", "/bin/p105", "/bin/p104", "/bin/p103"}},
		{"parent gone", 99, "", []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := invoker(test.pid, lookup); got.Exe != test.exe || !slices.Equal(got.Chain, test.chain) {
				t.Errorf("invoker(%d) = %q < %q, want %q < %q", test.pid, got.Exe, got.Chain, test.exe, test.chain)
			}
		})
	}
	long := process{path: "/Applications/" + strings.Repeat("é", maxPath)}
	if got := long.program(); len(got) > maxPath || !strings.HasPrefix(got, "/Applications/é") || !strings.HasSuffix(got, "é") {
		t.Errorf("a long path keeps %d bytes: %q", len(got), got)
	}
}

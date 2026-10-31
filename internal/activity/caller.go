package activity

import (
	"math"
	"os"
	"strings"
)

// Bounds that keep a record far below the size one write appends whole.
const (
	maxAncestors = 8
	maxPath      = 256
)

const launchdPID = 1

// process is what the kernel reports of one process.
type process struct {
	parent int
	// start orders processes by when they started.
	start int64
	// path is the executable's path, empty when the kernel withholds it;
	// name is the kernel's short name for it.
	path, name string
}

// Invoker describes the program that started process pid, which acts for
// it, and pid's controlling terminal.
func Invoker(pid int) Caller {
	c := invoker(pid, lookup)
	c.TTY = terminal(pid)
	return c
}

// Self describes this process, which acts for the person using its window.
func Self() Caller {
	c := lineage(os.Getpid(), math.MaxInt64, lookup)
	c.TTY = terminal(os.Getpid())
	return c
}

func invoker(pid int, lookup func(int) (process, bool)) Caller {
	p, ok := lookup(pid)
	if !ok {
		return Caller{Chain: []string{}}
	}
	return lineage(p.parent, p.start, lookup)
}

// lineage describes pid, which started no later than notAfter, and its
// ancestors. An ancestor that started after its child holds a pid reused
// since the child's parent exited, and ends the chain, as launchd does.
func lineage(pid int, notAfter int64, lookup func(int) (process, bool)) Caller {
	c := Caller{Chain: []string{}}
	p, ok := lookup(pid)
	if !ok || p.start > notAfter {
		return c
	}
	c.Exe = p.program()
	for len(c.Chain) < maxAncestors && p.parent > launchdPID {
		parent, ok := lookup(p.parent)
		if !ok || parent.start > p.start {
			break
		}
		c.Chain = append(c.Chain, parent.program())
		p = parent
	}
	return c
}

// program is p's executable path, or its name where the kernel withholds
// the path, with the head kept: it names the app bundle.
func (p process) program() string {
	program := p.path
	if program == "" {
		program = p.name
	}
	if len(program) <= maxPath {
		return program
	}
	return strings.ToValidUTF8(program[:maxPath], "")
}

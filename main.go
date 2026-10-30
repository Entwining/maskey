// Maskey is a credential manager: a window for people and the mk command
// line for coding agents and scripts, sharing credentials stored in iCloud
// Keychain.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/cli"
	"github.com/Entwining/maskey/internal/keychain"
	"github.com/Entwining/maskey/internal/runner"
	"github.com/Entwining/maskey/internal/store"
)

// mygo.json is the one source of the version, for the CLI as well: reading
// it from the bundle through MyGo would load the GUI backend.
//
//go:embed mygo.json
var config []byte

// skill is the agent skill that `mygo build` also copies into the app's
// resources, where installed agents link to it.
//
//go:embed resources/skills/maskey/SKILL.md
var skill []byte

func version() string {
	var c struct{ Version string }
	_ = json.Unmarshal(config, &c)
	return c.Version
}

func main() {
	// Command lines are handled before any GUI work, so commands run
	// without a window, and only a bare launch, as from Finder, opens one;
	// a bare mk prints its help.
	args := os.Args[1:]
	if len(args) == 0 && filepath.Base(os.Args[0]) != "mk" {
		runGUI()
		return
	}
	if len(args) > 0 && args[0] == runner.MonitorArg {
		if err := runner.Monitor(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "mk:", err)
			os.Exit(runner.ExitFailure)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	k := keychain.New(store.Service)
	if len(args) == 1 && args[0] == "--keychain-service" {
		err := agent.Serve(ctx, k, k.Check)
		stop()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	backend := agent.New(k)
	home, _ := os.UserHomeDir()
	outcome := cli.Run(ctx, args, cli.Env{
		Store:        store.New(backend),
		Check:        k.Check,
		CheckService: backend.CheckService,
		Setup: func(ctx context.Context) error {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			return agent.Install(ctx, executable)
		},
		Skill:       skill,
		SkillSource: skillSource(),
		Home:        home,
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Version:     version(),
	})
	stop()
	if outcome.Signal != 0 {
		exitBySignal(outcome.Signal)
	}
	os.Exit(outcome.Code)
}

// skillSource is the skill's directory in the bundle that holds this
// executable, which mk reaches through a link.
func skillSource() string {
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(executable)), "Resources", "skills", "maskey")
}

// exitBySignal ends the process the way the command it ran ended. The Go
// runtime cannot restore a signal's default action: it dies by a signal
// itself only for SIGHUP, SIGINT, and SIGTERM, and ignores others or prints a
// stack trace. A shell in this process's place sends the signal to itself
// with the default action, and if the signal is ignored, exits with the
// 128+n status a shell reports for it.
func exitBySignal(sig syscall.Signal) {
	n := int(sig)
	script := fmt.Sprintf("kill -%d $$; exit %d", n, 128+n)
	_ = syscall.Exec("/bin/sh", []string{"sh", "-c", script}, nil)
	os.Exit(128 + n)
}

// Maskey is a credential manager: a window for people and the mk command
// line for coding agents and scripts, sharing credentials stored in iCloud
// Keychain.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/cli"
	"github.com/Entwining/maskey/internal/keychain"
	"github.com/Entwining/maskey/internal/owner"
	"github.com/Entwining/maskey/internal/runner"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
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

func manifest() (m struct{ Name, Version string }) {
	_ = json.Unmarshal(config, &m)
	return m
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
	record := activityLog(func(err error) { fmt.Fprintln(os.Stderr, "mk: warning:", err) })
	k := keychain.New(store.Service)
	if len(args) == 1 && args[0] == agent.ServiceArg {
		dir, err := stores.Dir()
		if err == nil {
			registry := stores.NewRegistry(dir, k, k, remoteClient())
			// An approval the owner gave in the window does not let mk
			// change program access, nor the other way around.
			fromMk := owner.NewApproval(owner.NativeAuthenticate, owner.Clock, "program access changes from mk")
			fromWindow := owner.NewApproval(owner.NativeAuthenticate, owner.Clock, "program access changes in the Maskey window to entries outside iCloud Keychain")
			err = agent.Serve(ctx, registry, record, k.Check, func(ctx context.Context, reason string) (string, error) {
				if agent.FromWindow(ctx) {
					return fromWindow.Approve(ctx, reason)
				}
				return fromMk.Approve(ctx, reason)
			})
		}
		stop()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(args) == 1 && args[0] == agent.LegacyArg {
		// Installing ends this process, the earlier service's job, with the
		// signal launchd sends it. Handling that signal would cancel the
		// wait for the job to stop, which reads as a job that did not stop
		// and gets its configuration back. After a failure the process
		// waits, so that launchd does not start it again every few seconds;
		// mk setup then installs the service.
		stop()
		if err := agent.Install(context.Background(), executable()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			ended := make(chan os.Signal, 1)
			signal.Notify(ended, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
			<-ended
		}
		return
	}
	recordable := record.Writable()
	backend := agent.New(k, record, record.ReportFailure, recordable)
	home, _ := os.UserHomeDir()
	app := manifest()
	// The program that started mk is described before any operation, while
	// it surely runs.
	ctx = activity.WithCaller(ctx, activity.Invoker(os.Getpid()))
	outcome := cli.Run(ctx, args, cli.Env{
		OpenStore: func(ctx context.Context, id string) (cli.StoreAccess, error) {
			// iCloud Keychain, named, needs no configuration to be found.
			config, err := storesConfig(ctx, backend)
			if err != nil && id != stores.ICloud {
				return cli.StoreAccess{}, err
			}
			connection, err := config.Resolve(id)
			if err != nil {
				return cli.StoreAccess{}, err
			}
			client := backend
			if connection.Type != stores.ICloud {
				client = agent.NewRemote(connection, record, record.ReportFailure, recordable)
			}
			return cli.StoreAccess{Connection: connection, Store: store.New(client, record), ChangeAccess: client.ChangeProgramAccess}, nil
		},
		Stores:       func(ctx context.Context) (stores.Config, error) { return storesConfig(ctx, backend) },
		AddStore:     backend.AddStore,
		SetToken:     backend.SetStoreToken,
		ChangeStore:  backend.ChangeStore,
		Activity:     record,
		Check:        k.Check,
		CheckService: backend.CheckService,
		Setup: func(ctx context.Context) error {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			return agent.Install(ctx, executable)
		},
		ServiceItems:  agent.ServiceItems,
		RemoveService: agent.RemoveService,
		Skill:         skill,
		App:           app.Name,
		Executable:    executable(),
		Home:          home,
		Stdin:         os.Stdin,
		Terminal:      func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) },
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		Version:       app.Version,
	})
	stop()
	if outcome.Signal != 0 {
		exitBySignal(outcome.Signal)
	}
	os.Exit(outcome.Code)
}

// executable is the file in the app bundle that this process runs, which
// mk reaches through a link, or "" if it cannot be found.
func executable() string {
	path, err := os.Executable()
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return ""
	}
	return path
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

// remoteTimeout bounds one request to a remote store, so that a store that
// does not answer fails well within the time a client waits for the service.
const remoteTimeout = 15 * time.Second

// remoteClient makes the requests to a remote store. It follows no
// redirect: a request carries the store's access token or a session's, and
// following one would send that to wherever the answer points.
func remoteClient() *http.Client {
	return &http.Client{Timeout: remoteTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// storesConfig is this Mac's stores, which the Maskey service writes. A
// process that may not read their file, as in a sandbox, has the service
// read it, over the socket it reaches entries through. Without a home
// directory there is only iCloud Keychain.
func storesConfig(ctx context.Context, service interface {
	Stores(context.Context) (stores.Config, error)
}) (stores.Config, error) {
	dir, err := stores.Dir()
	if err != nil {
		return stores.Config{Default: stores.ICloud}, nil
	}
	config, err := stores.Load(dir)
	if errors.Is(err, stores.ErrDenied) {
		return service.Stores(ctx)
	}
	return config, err
}

// activityLog is this Mac's activity record, which reports a failure to
// record to warn. Without a home directory, every record fails.
func activityLog(warn func(error)) *activity.Log {
	dir, _ := activity.Dir(store.Service)
	return activity.New(dir, warn)
}

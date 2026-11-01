package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Entwining/maskey/internal/keychain"
	"github.com/Entwining/maskey/internal/store"
)

func doctorCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor [--json]",
		Short: "Check Keychain access, interpreter, service, and agent skill",
		Long: `Check whether mk can use the Keychain here.

Check Apple's Python interpreter, Keychain access directly or through the
service, whether the service runs this build, and the installed agent
skill. This reads entry attributes, never values. It cannot check iCloud
Keychain sync to other Macs.

Print one row per check. The first failed interpreter or Keychain check
determines the exit status; service and skill problems are warnings.
Check failures stay in the stdout report, including with --json.`,
		DisableFlagsInUseLine: true,
		Args:                  noArgs("doctor takes no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return doctor(cmd, env)
		},
	}
	addJSONFlag(cmd, "Write the check report as JSON")
	return cmd
}

type check struct {
	Check    string     `json:"check"`
	Status   string     `json:"status"`
	Detail   string     `json:"detail,omitempty"`
	Recovery string     `json:"recovery,omitempty"`
	Error    *errorBody `json:"error,omitempty"`
}

// doctor prints one row per check; a failed check whose kind decides the
// exit status is an error row, and the rest are warnings.
func doctor(cmd *cobra.Command, env *Env) error {
	ctx := cmd.Context()
	var checks []check
	var failure *store.Error
	fail := func(name string, err error) {
		var e *store.Error
		if !errors.As(err, &e) {
			e = &store.Error{Kind: store.KindKeychain, Message: err.Error()}
		}
		recovery, ok := doctorRecoveries[e.Kind]
		if !ok {
			recovery = recoveries[e.Kind]
		}
		checks = append(checks, check{Check: name, Status: "error", Error: &errorBody{Error: e, ExitCode: exitCodes[e.Kind], Recovery: recovery}})
		if failure == nil {
			failure = e
		}
	}
	if err := env.Check(ctx); err != nil {
		fail("interpreter", err)
	} else {
		checks = append(checks, check{Check: "interpreter", Status: "ok", Detail: keychain.Interpreter})
	}
	if namespaces, err := env.Store.Namespaces(ctx); err != nil {
		fail("keychain", err)
	} else {
		entries := 0
		for _, n := range namespaces {
			entries += n.Entries
		}
		checks = append(checks, check{Check: "keychain", Status: "ok", Detail: fmt.Sprintf("%d entries in %d namespaces", entries, len(namespaces))})
	}
	// Direct access, the keychain row, decides whether mk works; the service
	// adds grants, revokes, and access from a sandbox, so its row is a
	// warning.
	if err := env.CheckService(ctx); err != nil {
		var e *store.Error
		detail := err.Error()
		if errors.As(err, &e) {
			detail = e.Message
		}
		checks = append(checks, check{Check: "service", Status: "warning", Detail: detail, Recovery: "run mk setup in a normal terminal so mk grant, mk revoke, and sandboxed commands can reach the Keychain"})
	} else {
		checks = append(checks, check{Check: "service", Status: "ok", Detail: "running this build"})
	}
	checks = append(checks, skillCheck(env))

	var text strings.Builder
	for _, c := range checks {
		detail, recovery := c.Detail, c.Recovery
		if c.Error != nil {
			detail, recovery = c.Error.Error.Error(), c.Error.Recovery
		}
		fmt.Fprintf(&text, "%-8s %-12s %s\n", c.Status, c.Check, detail)
		if recovery != "" {
			fmt.Fprintf(&text, "%22shint: %s\n", "", recovery)
		}
	}
	if err := writeResult(cmd, env, map[string]any{"version": env.Version, "checks": checks}, text.String()); err != nil {
		return err
	}
	if failure != nil {
		return &exitError{Outcome{Code: exitCodes[failure.Kind]}}
	}
	return nil
}

func setupCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup [--json]",
		Short: "Install the Keychain service for sandboxes and program access",
		Long: `Install and start the per-user Keychain service, which serves sandboxed
commands and carries out mk grant and mk revoke.

Run outside the agent sandbox so the service can reach the Keychain.
The service verifies that callers are the same signed Maskey executable.
mk uses the service where direct Keychain access is unavailable, and for
the operations mk activity shows where it cannot write the activity
record. Commands stay in the caller's sandbox; grants and revokes always
use the service.

Maskey > Install Command Line Tool installs the service and the mk link.
mk setup installs only the service. After an update, the service moves to
the new build when the new mk connects.`,
		DisableFlagsInUseLine: true,
		Args:                  noArgs("setup takes no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.Setup(cmd.Context()); err != nil {
				var e *store.Error
				if !errors.As(err, &e) {
					err = &store.Error{Kind: store.KindPrerequisite, Message: err.Error()}
				}
				return err
			}
			return writeResult(cmd, env, map[string]bool{"installed": true}, "installed the Keychain service\n")
		},
	}
	addJSONFlag(cmd, "Write the result or errors as JSON")
	return cmd
}

// skillLinks are where agents look for user skills: Claude Code reads the
// first, and Codex and other agents following the Agent Skills layout the
// second.
func skillLinks(home string) []string {
	return []string{
		filepath.Join(home, ".claude", "skills", "maskey"),
		filepath.Join(home, ".agents", "skills", "maskey"),
	}
}

func skillCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "skill <command>",
		Short:                 "Show or install the agent skill",
		Long:                  "Show or install the agent skill shipped with this version of Maskey.",
		DisableFlagsInUseLine: true,
	}
	groupOfCommands(cmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the skill",
		Long: `Print the shipped agent skill as Markdown on standard output.

The skill matches this installed version of mk. This command reads no
Keychain entries and installs nothing.`,
		DisableFlagsInUseLine: true,
		Args:                  noArgs("skill show takes no arguments"),
		RunE: func(*cobra.Command, []string) error {
			if _, err := env.Stdout.Write(env.Skill); err != nil {
				return outputError()
			}
			return nil
		},
	})
	install := &cobra.Command{
		Use:   "install [--json]",
		Short: "Link the skill into the agent skill directories",
		Long: `Link the shipped skill into the agent skill directories.

Link ~/.claude/skills/maskey and ~/.agents/skills/maskey to the skill inside
Maskey.app. Existing links are replaced. Existing files and directories
are left alone with an exists error. If one link succeeds and another
fails, the completed link remains; retry after resolving the problem.`,
		DisableFlagsInUseLine: true,
		Args:                  noArgs("skill install takes no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			links, err := installSkill(env)
			if err != nil {
				return err
			}
			var text strings.Builder
			for _, link := range links {
				fmt.Fprintln(&text, "linked", link)
			}
			return writeResult(cmd, env, map[string]any{"links": links}, text.String())
		},
	}
	addJSONFlag(install, "Write the linked paths or errors as JSON")
	cmd.AddCommand(install)
	return cmd
}

// skillSource is the skill's directory in the app bundle that holds
// executable.
func skillSource(executable string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(executable)), "Resources", "skills", "maskey")
}

func installSkill(env *Env) ([]string, error) {
	source := skillSource(env.Executable)
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		return nil, &store.Error{Kind: store.KindPrerequisite, Message: "this mk is not inside Maskey.app, which holds the skill"}
	}
	links := skillLinks(env.Home)
	for _, link := range links {
		if info, err := os.Lstat(link); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return nil, &store.Error{Kind: store.KindExists, Message: link + " exists and is not a link"}
			}
			if target, _ := os.Readlink(link); target == source {
				continue
			}
			if err := os.Remove(link); err != nil {
				return nil, &store.Error{Kind: store.KindPrerequisite, Message: err.Error()}
			}
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return nil, &store.Error{Kind: store.KindPrerequisite, Message: err.Error()}
		}
		if err := os.Symlink(source, link); err != nil {
			return nil, &store.Error{Kind: store.KindPrerequisite, Message: err.Error()}
		}
	}
	return links, nil
}

func skillCheck(env *Env) check {
	source := skillSource(env.Executable)
	for _, link := range skillLinks(env.Home) {
		target, err := os.Readlink(link)
		switch {
		case err != nil:
			return check{Check: "skill", Status: "warning", Detail: "not installed", Recovery: "run mk skill install"}
		case target != source:
			return check{Check: "skill", Status: "warning", Detail: "links to another copy of Maskey", Recovery: "run mk skill install"}
		}
	}
	return check{Check: "skill", Status: "ok", Detail: "linked in ~/.claude/skills and ~/.agents/skills"}
}

// cliLink is where InstallCLI links mk.
func cliLink(home string) string { return filepath.Join(home, ".local", "bin", "mk") }

// InstallCLI links home/.local/bin/mk to the app's executable, so upgrading
// the app in place upgrades mk too. It replaces an earlier link but never a
// file.
func InstallCLI(home, executable string) (string, error) {
	target, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	link := cliLink(home)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return "", err
	}
	info, err := os.Lstat(link)
	switch {
	case err == nil && info.Mode()&fs.ModeSymlink == 0:
		return "", fmt.Errorf("%s exists and is not a symbolic link", link)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	// Symlink fails rather than replace a file already at this name, so
	// only a link made here is ever removed.
	temporary := fmt.Sprintf("%s.maskey-new-%d", link, os.Getpid())
	if err := os.Symlink(target, temporary); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, link); err != nil {
		os.Remove(temporary)
		return "", err
	}
	return link, nil
}

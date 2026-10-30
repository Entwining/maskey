package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Entwining/maskey/internal/keychain"
	"github.com/Entwining/maskey/internal/store"
)

func doctorCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that mk can use the Keychain here",
		Long: `Check that mk can use the Keychain here: Apple's Python interpreter, access
to the Keychain (directly or through the Keychain service), whether the
service runs this build of Maskey, and the agent skill. Prints one row per
check and exits with the status of the first failed interpreter or Keychain
check; problems with the service or the skill are warnings.

iCloud Keychain sync is not checked: Maskey sees only this Mac's Keychain,
and iCloud Keychain does not report when a change reaches other devices.`,
		DisableFlagsInUseLine: true,
		Args:                  noArgs("doctor takes no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return doctor(cmd, env)
		},
	}
	addJSONFlag(cmd, "the checks")
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
		checks = append(checks, check{Check: name, Status: "error", Error: &errorBody{e, recoveries[e.Kind]}})
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
	// The service matters only where direct access is unavailable, and then
	// the keychain row already fails, so the service's own row is a warning.
	if err := env.CheckService(ctx); err != nil {
		var e *store.Error
		detail := err.Error()
		if errors.As(err, &e) {
			detail = e.Message
		}
		checks = append(checks, check{Check: "service", Status: "warning", Detail: detail, Recovery: "run mk setup in a normal terminal so sandboxed commands can reach the Keychain"})
	} else {
		checks = append(checks, check{Check: "service", Status: "ok", Detail: "running this build"})
	}
	checks = append(checks, skillCheck(env))

	if jsonOutput(cmd) {
		writeJSON(env.Stdout, map[string]any{"version": env.Version, "checks": checks})
	} else {
		for _, c := range checks {
			detail, recovery := c.Detail, c.Recovery
			if c.Error != nil {
				detail, recovery = c.Error.Error.Error(), c.Error.Recovery
			}
			fmt.Fprintf(env.Stdout, "%-8s %-12s %s\n", c.Status, c.Check, detail)
			if recovery != "" {
				fmt.Fprintf(env.Stdout, "%22shint: %s\n", "", recovery)
			}
		}
	}
	if failure != nil {
		return &exitError{Outcome{Code: exitCodes[failure.Kind]}}
	}
	return nil
}

func setupCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the Keychain service for sandboxed commands",
		Long: `Install and start the per-user Keychain service, through which commands in
a sandbox reach the Keychain over a Unix socket. Run it in a normal
terminal; Maskey > Install Command Line Tool installs it too, along with the
mk link. After the app is replaced, the service restarts on the new build by
itself.`,
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
			if jsonOutput(cmd) {
				writeJSON(env.Stdout, map[string]bool{"installed": true})
			} else {
				fmt.Fprintln(env.Stdout, "installed the Keychain service")
			}
			return nil
		},
	}
	addJSONFlag(cmd, "the result")
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
		Use:   "skill",
		Short: "Show or install the agent skill",
		Long: `Show or install the agent skill that teaches coding agents to use mk. The
skill ships inside Maskey, so it matches this version of mk.`,
		DisableFlagsInUseLine: true,
	}
	groupOfCommands(cmd)
	cmd.AddCommand(&cobra.Command{
		Use:                   "show",
		Short:                 "Print the skill",
		DisableFlagsInUseLine: true,
		Args:                  noArgs("skill show takes no arguments"),
		RunE: func(*cobra.Command, []string) error {
			_, err := env.Stdout.Write(env.Skill)
			return err
		},
	})
	install := &cobra.Command{
		Use:   "install",
		Short: "Link the skill into the agent skill directories",
		Long: `Link ~/.claude/skills/maskey and ~/.agents/skills/maskey to the skill inside
Maskey, so agents load the version that matches the installed app. An
existing link is replaced; a directory or file there is left alone and
reported as existing.`,
		DisableFlagsInUseLine: true,
		Args:                  noArgs("skill install takes no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			links, err := installSkill(env)
			if err != nil {
				return err
			}
			if jsonOutput(cmd) {
				writeJSON(env.Stdout, map[string]any{"links": links})
			} else {
				for _, link := range links {
					fmt.Fprintln(env.Stdout, "linked", link)
				}
			}
			return nil
		},
	}
	addJSONFlag(install, "the result")
	cmd.AddCommand(install)
	return cmd
}

func installSkill(env *Env) ([]string, error) {
	if info, err := os.Stat(env.SkillSource); err != nil || !info.IsDir() {
		return nil, &store.Error{Kind: store.KindPrerequisite, Message: "this mk is not inside Maskey.app, which holds the skill"}
	}
	links := skillLinks(env.Home)
	for _, link := range links {
		if info, err := os.Lstat(link); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return nil, &store.Error{Kind: store.KindExists, Message: link + " exists and is not a link"}
			}
			if target, _ := os.Readlink(link); target == env.SkillSource {
				continue
			}
			if err := os.Remove(link); err != nil {
				return nil, &store.Error{Kind: store.KindPrerequisite, Message: err.Error()}
			}
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return nil, &store.Error{Kind: store.KindPrerequisite, Message: err.Error()}
		}
		if err := os.Symlink(env.SkillSource, link); err != nil {
			return nil, &store.Error{Kind: store.KindPrerequisite, Message: err.Error()}
		}
	}
	return links, nil
}

func skillCheck(env *Env) check {
	for _, link := range skillLinks(env.Home) {
		target, err := os.Readlink(link)
		switch {
		case err != nil:
			return check{Check: "skill", Status: "warning", Detail: "not installed", Recovery: "run mk skill install"}
		case target != env.SkillSource:
			return check{Check: "skill", Status: "warning", Detail: "links to another copy of Maskey", Recovery: "run mk skill install"}
		}
	}
	return check{Check: "skill", Status: "ok", Detail: "linked in ~/.claude/skills and ~/.agents/skills"}
}

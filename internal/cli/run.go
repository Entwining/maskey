package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/runner"
	"github.com/Entwining/maskey/internal/sshagent"
	"github.com/Entwining/maskey/internal/store"
)

func runCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "run [--json] <selector>... -- <command> [<arg>...]",
		SuggestFor: []string{"exec", "env"},
		Short:      "Run a command with selected credentials and mask its output",
		Long: `Each selector gives the command one entry (github/GITHUB_TOKEN) or a
whole namespace (github). Put mk flags before the first selector. The
first -- is required. Everything after it belongs to the command.

Each entry sets NAME in the child's environment, replacing any inherited
value. An explicit entry overrides all namespace entries with that NAME.
Two namespaces that still supply one NAME conflict, as do two different
explicit entries with one NAME. Repeating a selector has no effect.
A missing entry or empty namespace stops mk before the command starts.

An entry of kind ssh sets NAME to its public key instead. mk keeps the
private key and signs with it for the command through an SSH agent at
SSH_AUTH_SOCK, for ssh, git, and ssh-keygen -Y sign, until the command
ends. An IdentityAgent in ssh_config takes precedence over SSH_AUTH_SOCK,
and ssh -A lends the agent to the remote host too.

mk runs the command directly. For shell syntax, use sh -c '...' so the
child shell interprets the script. Keep credentials out of child arguments.

mk masks exact values in the child's stdout and stderr. Encoding, styling,
or inserting line breaks can expose a value. Files and network traffic
are outside the mask. A partial match can delay output. Run commands you
trust with the credentials you select.

When stdin and stdout are the same foreground terminal, interactive
programs get a terminal. Ctrl-Z and fg suspend and resume them with mk.
Redirected stderr stays separate. Processes left behind lose that terminal
when the command ends. Other invocations, and values containing control
characters, use pipes for output.

mk returns the child's status or ends by the same signal. Its own failures
use 125, 126, or 127. See mk help exit-codes.`,
		Example: `  mk run github -- gh auth status
  mk run github/GITHUB_TOKEN -- gh pr list
  mk run github work/GITHUB_TOKEN -- gh pr list`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ArbitraryArgs,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
			// Cobra's completion parses the flags twice, which leaves
			// ArgsLenAtDash stale; a -- that pflag consumed had no selector
			// before it, so the line is wrong either way.
			if slices.Contains(args, "--") {
				// The command's arguments are the shell's to complete.
				return nil, cobra.ShellCompDirectiveDefault
			}
			return completeNames(cmd.Context(), env, args, true)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			selectors, command, err := runOperands(cmd, args)
			if err != nil {
				return err
			}
			entries, err := env.Store.Values(activity.WithVia(cmd.Context(), "run"), selectors)
			if err != nil {
				return err
			}
			entries, keys, variables, err := sshKeys(entries)
			if err != nil {
				return err
			}
			var exited func()
			if len(keys) > 0 {
				sshAgent, err := sshagent.Start(keys)
				if err != nil {
					return withHint(&store.Error{Kind: kindRunFailed, Message: "the SSH agent could not start: " + err.Error()},
						"let the sandbox create Unix sockets in TMPDIR, or set TMPDIR to a directory where it can")
				}
				defer sshAgent.Close()
				exited = func() { sshAgent.Close() }
				variables = append(variables, "SSH_AUTH_SOCK="+sshAgent.Socket())
			}
			exit, err := runner.Run(cmd.Context(), runner.Command{
				Path:      command[0],
				Args:      command[1:],
				Entries:   entries,
				Variables: variables,
				Stdin:     env.Stdin,
				Stdout:    env.Stdout,
				Stderr:    env.Stderr,
				Exited:    exited,
			})
			if errors.Is(err, context.Canceled) {
				return &store.Error{Kind: store.KindCancelled, Message: "cancelled before the command started"}
			}
			var runErr *runner.Error
			if errors.As(err, &runErr) {
				kind := kindRunFailed
				switch runErr.Code {
				case runner.ExitNotFound:
					kind = kindNotFound
				case runner.ExitNotExecutable:
					kind = kindNotExecutable
				}
				failure := &store.Error{Kind: kind, Message: runErr.Message}
				switch {
				case errors.Is(runErr, exec.ErrDot):
					return withHint(failure, "give its path, such as ./<command>")
				case errors.Is(runErr, syscall.EISDIR):
					return withHint(failure, "give the path of the executable inside it")
				case errors.Is(runErr, syscall.ENOEXEC):
					return withHint(failure, "add a #! line to a script, or run it through its interpreter, such as sh <file>")
				}
				return failure
			}
			if err != nil {
				return err
			}
			if exit.Code == 0 && exit.Signal == 0 {
				return nil
			}
			return &exitError{Outcome(exit)}
		},
	}
	// Flags stop at the first selector, so none of the command's arguments
	// is taken for one of mk's.
	cmd.Flags().SetInterspersed(false)
	addJSONFlag(cmd, "Write mk's own errors as JSON")
	return cmd
}

// sshKeys takes the entries of kind ssh out of entries, as keys for an SSH
// agent and their public keys as the variables that set their names.
func sshKeys(entries []store.Entry) ([]store.Entry, []agent.AddedKey, []string, error) {
	var values []store.Entry
	var keys []agent.AddedKey
	var variables []string
	for _, entry := range entries {
		if entry.Kind != store.ValueSSH {
			values = append(values, entry)
			continue
		}
		key, public, err := sshagent.Key(entry.Value)
		if err != nil {
			return nil, nil, nil, sshKeyError(entry.Ref, err, fmt.Sprintf("replace it with mk set --stdin %s < <file>, or choose another kind in Maskey", entry.Ref))
		}
		keys = append(keys, agent.AddedKey{PrivateKey: key, Comment: entry.String()})
		variables = append(variables, entry.Name+"="+strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(public)), "\n"))
	}
	return values, keys, variables, nil
}

// sshKeyError is the error of ref's value, which sshagent.Key refused with
// err; hint is how to replace a value that is no SSH private key.
func sshKeyError(ref store.Ref, err error, hint string) error {
	switch {
	case errors.Is(err, sshagent.ErrPassphrase):
		return sshPassphraseError(ref)
	case errors.Is(err, sshagent.ErrPKCS8):
		return withHint(&store.Error{Kind: store.KindInvalid, Ref: &ref, Message: "the private key is protected by a passphrase that mk cannot remove"},
			fmt.Sprintf("openssl pkey -in <file> | mk set --stdin %s stores the key without it, with OpenSSL asking for the passphrase on the terminal", ref))
	case errors.Is(err, sshagent.ErrUnsupported):
		return withHint(&store.Error{Kind: store.KindInvalid, Ref: &ref, Message: "the SSH agent cannot lend this private key"},
			"store the whole key, of type Ed25519, RSA, or ECDSA P-256, P-384, or P-521; for a program that reads the key itself, choose another kind in Maskey")
	}
	return withHint(&store.Error{Kind: store.KindInvalid, Ref: &ref, Message: "the value of an entry of kind ssh is not an SSH private key"}, hint)
}

func sshPassphraseError(ref store.Ref) error {
	return withHint(&store.Error{Kind: store.KindInvalid, Ref: &ref, Message: "the SSH key is protected by a passphrase, which mk run does not ask for"},
		fmt.Sprintf("mk set --stdin %s < <file> in a terminal stores the key without it", ref))
}

// runOperands splits run's operands at the first -- into selectors, each an
// entry or with an empty name a namespace, and the command. pflag records
// the -- itself only when no selector precedes it.
func runOperands(cmd *cobra.Command, args []string) ([]store.Ref, []string, error) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		if dash = slices.Index(args, "--"); dash < 0 {
			return nil, nil, usageError("run needs -- before the command")
		}
		args = slices.Concat(args[:dash], args[dash+1:])
	}
	operands, command := args[:dash], args[dash:]
	for _, operand := range operands {
		flag, ok := strings.CutPrefix(operand, "-")
		// A lone - is an operand, as pflag takes it.
		if !ok || flag == "" {
			continue
		}
		// The name of a long flag ends at its value, and a short one is a
		// letter, which its value may follow.
		if long, ok := strings.CutPrefix(flag, "-"); ok {
			flag, _, _ = strings.Cut(long, "=")
		} else {
			flag = flag[:1]
		}
		if isSelectorFlag(flag) {
			return nil, nil, selectorFlagError()
		}
		return nil, nil, usageError("mk flags must precede the first selector")
	}
	switch {
	case len(operands) == 0:
		return nil, nil, usageError("run needs at least one namespace or entry before --")
	case len(command) == 0:
		return nil, nil, usageError("run needs a command after --")
	}
	selectors, err := parseSelectors(operands)
	if err != nil {
		return nil, nil, err
	}
	return selectors, command, nil
}

func isSelectorFlag(name string) bool {
	return name == "n" || name == "namespace" || name == "e" || name == "entry"
}

func selectorFlagError() error {
	return withHint(usageError("run no longer accepts -n, --namespace, -e, or --entry"),
		"use mk run github -- <command> for a namespace or mk run github/GITHUB_TOKEN -- <command> for one entry")
}

package cli

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/runner"
	"github.com/Entwining/maskey/internal/store"
)

func runCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "run [--json] <selector>... -- <command> [<arg>...]",
		SuggestFor: []string{"exec", "env"},
		Short:      "Run a command with selected credentials and mask its output",
		Long: `Give the command one entry (github/GITHUB_TOKEN) or a whole namespace
(github). Put mk flags before the first selector. The first -- is
required. Everything after it belongs to the command.

Each entry sets NAME in the child's environment, replacing any inherited
value. An explicit entry overrides all namespace entries with that NAME.
Two namespaces that still supply one NAME conflict, as do two different
explicit entries with one NAME. Repeating a selector has no effect.
A missing entry or empty namespace stops mk before the command starts.

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
			exit, err := runner.Run(cmd.Context(), runner.Command{
				Path:    command[0],
				Args:    command[1:],
				Entries: entries,
				Stdin:   env.Stdin,
				Stdout:  env.Stdout,
				Stderr:  env.Stderr,
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
				return &store.Error{Kind: kind, Message: runErr.Message}
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

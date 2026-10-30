package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/Entwining/maskey/internal/runner"
	"github.com/Entwining/maskey/internal/store"
)

func runCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "run [-n <namespace>]... [-e <namespace>/<name>]... -- <command> [<arg>...]",
		SuggestFor: []string{"exec", "env", "get"},
		Short:      "Run a command with entries as environment variables",
		Long: `Run a command with entries as environment variables: every entry of each
namespace -n selects, and each entry -e names. Each of their values is
replaced in the command's stdout and stderr with a marker, usually ***.

The command runs without a shell: to expand variables or use pipes, run
sh -c '...' as the command. A name selected from two namespaces is a
conflict. mk exits with the command's status, or ends by the same signal;
when mk itself fails it exits 125, 126 or 127 (see "mk help exit-codes").

Masking covers exact values in stdout and stderr only, not encoded values,
files, or network traffic. Programs that need a terminal for their output,
such as full-screen programs, do not work.`,
		Example: `  mk run -n github -- gh auth status
  mk run -n aws -e github/GITHUB_TOKEN -- sh -c 'echo "$AWS_REGION"'`,
		DisableFlagsInUseLine: true,
		Args: func(cmd *cobra.Command, args []string) error {
			switch {
			case cmd.ArgsLenAtDash() < 0 && len(args) > 0:
				return usageError("run needs -- before the command")
			case cmd.ArgsLenAtDash() != 0:
				return usageError("run takes -n and -e options, then -- and the command")
			case len(args) == 0:
				return usageError("run needs a command after --")
			}
			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if cmd.ArgsLenAtDash() >= 0 {
				return nil, cobra.ShellCompDirectiveDefault
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			namespaces, _ := cmd.Flags().GetStringArray("namespace")
			selected, _ := cmd.Flags().GetStringArray("entry")
			refs := make([]store.Ref, len(selected))
			for i, arg := range selected {
				ref, err := parseEntry(arg)
				if err != nil {
					return err
				}
				refs[i] = ref
			}
			entries, err := env.Store.Values(cmd.Context(), namespaces, refs)
			if err != nil {
				return err
			}
			exit, err := runner.Run(cmd.Context(), runner.Command{
				Path:    args[0],
				Args:    args[1:],
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
	// Options after the first operand belong to the command.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringArrayP("namespace", "n", nil, "pass the entries of this `namespace`; repeat for more")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completeNamespaces(env))
	cmd.Flags().StringArrayP("entry", "e", nil, "pass this `entry`, <namespace>/<name>; repeat for more")
	_ = cmd.RegisterFlagCompletionFunc("entry", completeEntries(env))
	addJSONFlag(cmd, "mk's own errors")
	return cmd
}

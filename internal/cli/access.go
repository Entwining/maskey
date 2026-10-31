package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Entwining/maskey/internal/owner"
	"github.com/Entwining/maskey/internal/store"
)

func grantCommand(env *Env) *cobra.Command {
	return accessCommand(env, true, &cobra.Command{
		Use:   "grant [--json] <namespace>/<NAME>...",
		Short: "Let any program obtain entries' values with mk get",
		Long: `Grant entries program access, so that mk get writes their values for any
program that can run mk. Only the owner of this Mac grants it: macOS asks
for their login password, Touch ID, or Apple Watch.`,
		Example: "  mk grant anthropic/ANTHROPIC_API_KEY",
	})
}

func revokeCommand(env *Env) *cobra.Command {
	return accessCommand(env, false, &cobra.Command{
		Use:   "revoke [--json] <namespace>/<NAME>...",
		Short: "Take program access away from entries",
		Long: `Revoke entries' program access, so that mk get refuses them again. mk run
is unaffected. Like granting, revoking asks the owner of this Mac for their
login password, Touch ID, or Apple Watch.`,
		Example: "  mk revoke anthropic/ANTHROPIC_API_KEY",
	})
}

func accessCommand(env *Env, granted bool, cmd *cobra.Command) *cobra.Command {
	verb, done := "revoke", "revoked"
	if granted {
		verb, done = "grant", "granted"
	}
	cmd.Long += `

One approval covers further grants and revokes by mk for 5 minutes; mk
notes on stderr when a change needed no dialog. Entries already in the
requested state are reported unchanged and need no approval. Cancelling
the dialog, or interrupting mk while it is open, changes nothing; once the
owner approves, mk finishes the changes. They go through the Keychain
service that mk setup installs.

Each reference may appear once. mk changes entries in argument order and
stops at the first failure. Earlier changes remain.`
	cmd.DisableFlagsInUseLine = true
	cmd.Args = countArgs(verb+" needs at least one entry", 1, -1)
	cmd.ValidArgsFunction = completeEntries(env, -1)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		refs, err := parseEntries(args)
		if err == nil {
			err = distinct(refs)
		}
		if err != nil {
			return err
		}
		changes, auth, err := env.ChangeAccess(cmd.Context(), refs, granted)
		results := make([]result, len(changes))
		for i, change := range changes {
			results[i] = result{change.Ref, "unchanged"}
			if change.Changed {
				results[i].Action = done
			}
		}
		var e *store.Error
		switch {
		case errors.As(err, &e) && e.Ref != nil && len(changes) < len(refs) && *e.Ref == refs[len(changes)]:
			// A failure that names an entry is the change to it. The error
			// lists the completed changes too, so it stands if stdout fails.
			if !jsonOutput(cmd) {
				_ = writeResults(env, results)
				reportReuse(env, auth)
			}
			failure := stopped(atArgument(err, len(changes)+1), results, len(changes)+1, len(refs))
			failure.Auth = auth
			return failure
		case store.IsKind(err, store.KindUnavailable):
			return withHint(err, "run mk setup; program access changes only through the Keychain service")
		case err != nil:
			return err
		}
		if jsonOutput(cmd) {
			document := map[string]any{"results": results}
			if auth != "" {
				document["auth"] = auth
			}
			if err := writeJSON(env.Stdout, document); err != nil {
				return withHint(err, outputHint)
			}
			return nil
		}
		if err := writeResults(env, results); err != nil {
			return err
		}
		reportReuse(env, auth)
		return nil
	}
	addJSONFlag(cmd, "Write results or errors as JSON")
	return cmd
}

// reportReuse says when changes needed no dialog.
func reportReuse(env *Env, auth string) {
	if auth == owner.Reused {
		fmt.Fprintln(env.Stderr, "mk: no dialog: the owner approved a change to program access in the last 5 minutes")
	}
}

func writeResults(env *Env, results []result) error {
	for _, r := range results {
		if _, err := fmt.Fprintln(env.Stdout, r.Action, r.Ref); err != nil {
			return withHint(outputError(), outputHint)
		}
	}
	return nil
}

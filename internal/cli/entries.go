package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Entwining/maskey/internal/store"
)

func listCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "list [--json] [<namespace>]",
		SuggestFor: []string{"ls"},
		Short:      "List entry references",
		Long: `List entries without values, sorted by namespace and name.

Print one <namespace>/<NAME> per line, with no heading. A namespace with
no entries is not found. An empty unfiltered text list succeeds with empty
stdout and mk: no entries on stderr.
Values and notes are never printed.

JSON adds kind, modified when known, and program_access. program_access
means any program can get the raw value; only the owner can change it.
The result is {"entries":[...]}; an empty result is {"entries":[]} with
no stderr message.`,
		Example: `  mk list
  mk list github
  mk list --json github`,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("list takes at most one namespace", 0, 1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			namespaces, err := env.Store.Namespaces(cmd.Context())
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			names := make([]cobra.Completion, len(namespaces))
			for i, n := range namespaces {
				names[i] = n.Name
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := store.ValidateNamespace(args[0]); err != nil {
					return atArgument(err, 1)
				}
			}
			_, refs, attributes, err := env.Store.Listing(cmd.Context())
			if err != nil {
				return err
			}
			if len(args) == 1 {
				refs = slices.DeleteFunc(refs, func(ref store.Ref) bool { return ref.Namespace != args[0] })
				if len(refs) == 0 {
					return &store.Error{Kind: store.KindNotFound, Argument: 1, Message: "the namespace has no entries"}
				}
			}
			type entry struct {
				store.Ref
				Kind          store.ValueKind `json:"kind"`
				Modified      time.Time       `json:"modified,omitzero"`
				ProgramAccess bool            `json:"program_access"`
			}
			entries := make([]entry, len(refs))
			var text strings.Builder
			for i, ref := range refs {
				a := attributes[ref]
				entries[i] = entry{ref, store.ValueKindOf(ref.Name, a.Kind), a.Modified.UTC(), a.ProgramAccess}
				fmt.Fprintln(&text, ref)
			}
			if len(refs) == 0 && !jsonOutput(cmd) {
				fmt.Fprintln(env.Stderr, "mk: no entries")
			}
			return writeResult(cmd, env, map[string]any{"entries": entries}, text.String())
		},
	}
	addJSONFlag(cmd, "Write entries or errors as JSON")
	return cmd
}

func setCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use: "set",
		Annotations: map[string]string{usageAnnotation: `mk set [--json] <namespace>/<NAME>...
mk set --stdin [--json] <namespace>/<NAME>`},
		SuggestFor: []string{"add", "put", "create"},
		Short:      "Create entries or replace their values",
		Long: `Create entries or replace their values. By default, type one value per
entry at a hidden prompt, in argument order. This requires terminal stdin.

With --stdin, pipe or redirect one value for one entry. A one-line value
loses its final line ending, so gh auth token | mk set --stdin
github/GITHUB_TOKEN stores the token alone. A value of several lines,
such as a PEM key, is stored byte for byte. Empty values and NUL bytes
are rejected.

A namespace starts with an ASCII letter or digit, followed by ASCII
letters, digits, '.', '_' or '-'. NAME starts with an ASCII letter or
'_', followed by ASCII letters, digits or '_'. Each reference may appear
once.

mk checks all references and reads and validates all values before
writing. It writes in order and stops at the first failure. Earlier
writes remain. Replacing a value preserves its kind, notes, and program
access. New entries start without program access.`,
		Example: `  mk set aws/AWS_ACCESS_KEY_ID aws/AWS_SECRET_ACCESS_KEY
  gh auth token | mk set --stdin github/GITHUB_TOKEN`,
		Args:              countArgs("set needs at least one entry", 1, -1),
		ValidArgsFunction: completeEntries(env, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			refs, err := parseEntries(args)
			if store.IsKind(err, kindUsage) {
				// An operand that is no entry is likely the value itself.
				return withHint(err, "enter the value at the hidden prompt or pipe one value to mk set --stdin <namespace>/<NAME>")
			}
			if err == nil {
				err = distinct(refs)
			}
			if err != nil {
				return err
			}
			fromStdin, _ := cmd.Flags().GetBool("stdin")
			switch {
			case fromStdin && len(refs) > 1:
				return withHint(usageError("set --stdin takes exactly one entry"), "usage: mk set --stdin [--json] <namespace>/<NAME>")
			case !fromStdin && !term.IsTerminal(int(env.Stdin.Fd())):
				return withHint(usageError("standard input is not a terminal and set needs --stdin"),
					"pipe one value to mk set --stdin <namespace>/<NAME>, or have the owner run mk set <namespace>/<NAME> in their terminal")
			}
			values := make([][]byte, len(refs))
			for i, ref := range refs {
				value, err := readValue(cmd.Context(), env, ref, i, len(refs), fromStdin)
				if err != nil {
					return err
				}
				if err := store.ValidateValue(value); err != nil {
					return atArgument(err, i+1)
				}
				values[i] = value
			}
			return writeEach(cmd, env, refs, func(i int, ref store.Ref) (string, error) {
				created, err := env.Store.Set(cmd.Context(), ref, values[i])
				if created {
					return "created", err
				}
				return "replaced", err
			})
		},
	}
	cmd.Flags().Bool("stdin", false, "Read one value from standard input")
	addJSONFlag(cmd, "Write results or errors as JSON")
	return cmd
}

func getCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "get [--json] <namespace>/<NAME>",
		SuggestFor: []string{"read", "print"},
		Short:      "Write a granted entry's raw value to non-terminal stdout",
		Long: `Use mk run <namespace>/<NAME> -- <command> when the command accepts an
environment variable. Use mk get for a helper that must capture stdout.

Only the owner can grant an entry program access. Once granted, any
program that can run mk with access to that Keychain can obtain the
value. The grant applies to the entry, across consumers.

get refuses terminal stdout and entries without program access. On
success it writes the unmasked bytes exactly as stored, with no added
newline, label, or JSON wrapper.`,
		Annotations:           map[string]string{examplesHeadingAnnotation: "Helper command to configure:"},
		Example:               "  mk get anthropic/ANTHROPIC_API_KEY",
		DisableFlagsInUseLine: true,
		Args:                  countArgs("get takes exactly one entry", 1, 1),
		ValidArgsFunction:     completeEntries(env, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseEntry(args[0], 1)
			if err != nil {
				return err
			}
			// Checked before the lookup, so a refused value is never read.
			if f, ok := env.Stdout.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
				return withHint(usageError("get cannot write a raw value to terminal stdout"),
					"use mk run <namespace>/<NAME> -- <command>, or configure the helper to capture mk get directly. To view the value yourself, use Reveal in Maskey.")
			}
			value, err := env.Store.GrantedValue(cmd.Context(), ref)
			if err != nil {
				return atArgument(err, 1)
			}
			if _, err := env.Stdout.Write(value); err != nil {
				return outputError()
			}
			return nil
		},
	}
	addJSONFlag(cmd, "Write errors as JSON; the value on stdout stays raw")
	return cmd
}

func renameCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "rename [--json] <namespace>/<NAME> <new-namespace>/<NEW_NAME>",
		SuggestFor: []string{"mv", "move"},
		Short:      "Rename an entry or move it to another namespace",
		Long: `Rename an entry or move it to another namespace.

Change the reference in one write. The value, kind, notes, and program
access stay. The destination must differ and must not already exist.

A namespace starts with an ASCII letter or digit, followed by ASCII
letters, digits, '.', '_' or '-'. NAME starts with an ASCII letter or
'_', followed by ASCII letters, digits or '_'.`,
		Example: `  mk rename github/TOKEN github/GITHUB_TOKEN
  mk rename work/NPM_TOKEN npm/NPM_TOKEN`,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("rename takes the entry and its new reference", 2, 2),
		ValidArgsFunction:     completeEntries(env, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			refs, err := parseEntries(args)
			if err != nil {
				return err
			}
			from, to := refs[0], refs[1]
			if from == to {
				// Edit takes an unchanged entry as done without looking for it.
				return withHint(usageError("the new reference is the entry's own"), "choose a different namespace or name")
			}
			if err := env.Store.Edit(cmd.Context(), from, to, nil, nil); err != nil {
				if store.IsKind(err, store.KindExists) {
					return atArgument(err, 2)
				}
				return atArgument(err, 1)
			}
			return writeResult(cmd, env, map[string]any{"from": from, "to": to}, fmt.Sprintf("renamed %s to %s\n", from, to))
		},
	}
	addJSONFlag(cmd, "Write the result or errors as JSON")
	return cmd
}

func deleteCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "delete [--yes] [--json] <namespace>/<NAME>...",
		SuggestFor: []string{"rm", "remove"},
		Short:      "Delete entries from this Mac and Macs syncing its Keychain",
		Long: `Delete entries from this Mac and, through iCloud Keychain, other Macs
syncing this Keychain. Deletion cannot be undone.

mk checks that every entry exists, lists the entries, and asks once.
Type y or yes and press Return to confirm, ignoring case. Without terminal
stdin, --yes is required. Use it only for entries the owner asked to delete.

Each reference may appear once. mk deletes in argument order and stops
at the first failure. Earlier deletions remain. An entry can change after
the initial check.`,
		Example: `  mk delete github/OLD_TOKEN npm/OLD_TOKEN
  mk delete --yes --json github/OLD_TOKEN`,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("delete needs at least one entry", 1, -1),
		ValidArgsFunction:     completeEntries(env, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			refs, err := parseEntries(args)
			if err == nil {
				err = distinct(refs)
			}
			if err != nil {
				return err
			}
			yes, _ := cmd.Flags().GetBool("yes")
			if !yes && !term.IsTerminal(int(env.Stdin.Fd())) {
				return withHint(usageError("standard input is not a terminal and delete needs --yes"),
					"use --yes only for the entries the owner asked to delete")
			}
			_, existing, _, err := env.Store.Listing(cmd.Context())
			if err != nil {
				return err
			}
			for i, ref := range refs {
				if !slices.Contains(existing, ref) {
					return &store.Error{Kind: store.KindNotFound, Argument: i + 1, Message: "the entry does not exist"}
				}
			}
			if !yes {
				if err := confirmDelete(cmd.Context(), env, refs); err != nil {
					return err
				}
			}
			return writeEach(cmd, env, refs, func(_ int, ref store.Ref) (string, error) {
				return "deleted", env.Store.Delete(cmd.Context(), ref)
			})
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	addJSONFlag(cmd, "Write results or errors as JSON")
	return cmd
}

// result is a write of set or delete that the Keychain confirmed.
type result struct {
	store.Ref
	Action string `json:"action"`
}

const outputHint = "check the output destination before continuing. Do not repeat confirmed writes."

// writeEach writes refs in order, each with write, which returns the action
// it performs, and stops at the first failure. In text, each confirmed
// write is printed as it happens; with --json, all of them once every write
// is done. A failure after the first write was attempted reports the
// confirmed writes and the positions not attempted.
func writeEach(cmd *cobra.Command, env *Env, refs []store.Ref, write func(i int, ref store.Ref) (string, error)) error {
	completed := []result{}
	stop := func(err error, next int) error { return stopped(err, completed, next, len(refs)) }
	for i, ref := range refs {
		if cmd.Context().Err() != nil {
			cancelled := &store.Error{Kind: store.KindCancelled, Message: "cancelled before any entry was written"}
			if i == 0 {
				return cancelled
			}
			cancelled.Message = "cancelled before the next write"
			return stop(cancelled, i)
		}
		action, err := write(i, ref)
		if err != nil {
			return stop(atArgument(err, i+1), i+1)
		}
		completed = append(completed, result{ref, action})
		if !jsonOutput(cmd) {
			if err := writeResults(env, completed[i:]); err != nil {
				return stop(err, i+1)
			}
		}
	}
	if jsonOutput(cmd) {
		if err := writeJSON(env.Stdout, map[string]any{"results": completed}); err != nil {
			return stop(withHint(err, outputHint), len(refs))
		}
	}
	return nil
}

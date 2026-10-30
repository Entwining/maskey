package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Entwining/maskey/internal/store"
)

func listCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "list [<namespace>]",
		SuggestFor: []string{"ls"},
		Short:      "List entries",
		Long: `List entries by reference, one per line, sorted. With a namespace, list only
its entries; a namespace without entries is not found. --json adds each
entry's kind and modification time. Values and notes are never printed.`,
		Example: `  mk list
  mk list github`,
		DisableFlagsInUseLine: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageError("list takes at most one namespace")
			}
			if len(args) == 1 {
				return store.ValidateNamespace(args[0])
			}
			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return completeNamespaces(env)(cmd, args, toComplete)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			_, refs, attributes, err := env.Store.Listing(cmd.Context())
			if err != nil {
				return err
			}
			if len(args) == 1 {
				refs = slices.DeleteFunc(refs, func(ref store.Ref) bool { return ref.Namespace != args[0] })
				if len(refs) == 0 {
					return &store.Error{Kind: store.KindNotFound, Message: fmt.Sprintf("namespace %q has no entries", args[0])}
				}
			}
			if jsonOutput(cmd) {
				type entry struct {
					store.Ref
					Kind     store.ValueKind `json:"kind"`
					Modified time.Time       `json:"modified,omitzero"`
				}
				entries := make([]entry, len(refs))
				for i, ref := range refs {
					entries[i] = entry{ref, store.ValueKindOf(ref.Name, attributes[ref].Kind), attributes[ref].Modified.UTC()}
				}
				writeJSON(env.Stdout, map[string]any{"entries": entries})
				return nil
			}
			if len(refs) == 0 {
				fmt.Fprintln(env.Stderr, "mk: no entries")
			}
			for _, ref := range refs {
				fmt.Fprintln(env.Stdout, ref)
			}
			return nil
		},
	}
	addJSONFlag(cmd, "the entries")
	return cmd
}

func setCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "set [--stdin] <namespace>/<name>",
		SuggestFor: []string{"add", "put", "create"},
		Short:      "Create an entry or replace its value",
		Long: `Create an entry or replace its value. The value comes from a hidden prompt,
or with --stdin from standard input. It is stored byte for byte: a trailing
newline is kept, so pipe it with printf '%s'. A new entry's kind is inferred
from its name, such as token for GITHUB_TOKEN; change it in the Maskey window.`,
		Example: `  mk set github/GITHUB_TOKEN
  printf '%s' "$(gh auth token)" | mk set --stdin github/GITHUB_TOKEN`,
		DisableFlagsInUseLine: true,
		Args:                  entryArgs("set takes one entry, such as github/GITHUB_TOKEN; the value comes from the prompt or --stdin", 1),
		ValidArgsFunction:     completeEntries(env),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, _ := parseEntry(args[0])
			fromStdin, _ := cmd.Flags().GetBool("stdin")
			value, err := readValue(cmd.Context(), env, ref, fromStdin)
			if err != nil {
				return err
			}
			created, err := env.Store.Set(cmd.Context(), ref, value)
			if err != nil {
				return err
			}
			if jsonOutput(cmd) {
				writeJSON(env.Stdout, map[string]any{"namespace": ref.Namespace, "name": ref.Name, "created": created})
			} else if created {
				fmt.Fprintln(env.Stdout, "created", ref)
			} else {
				fmt.Fprintln(env.Stdout, "replaced", ref)
			}
			return nil
		},
	}
	cmd.Flags().Bool("stdin", false, "read the value from standard input")
	addJSONFlag(cmd, "the result")
	return cmd
}

// readValue reads the value of ref from stdin, or from a hidden prompt on
// a terminal.
func readValue(ctx context.Context, env *Env, ref store.Ref, fromStdin bool) ([]byte, error) {
	var read func() ([]byte, error)
	fd := int(env.Stdin.Fd())
	switch {
	case fromStdin:
		read = func() ([]byte, error) { return io.ReadAll(env.Stdin) }
	case term.IsTerminal(fd):
		// ReadPassword turns echo back on only when it returns, which a
		// cancelled read never does.
		state, err := term.GetState(fd)
		if err != nil {
			return nil, usageError("cannot read the value from the terminal: " + err.Error())
		}
		defer func() { _ = term.Restore(fd, state) }()
		fmt.Fprintf(env.Stderr, "Value for %s (input hidden): ", ref)
		defer fmt.Fprintln(env.Stderr)
		read = func() ([]byte, error) { return term.ReadPassword(fd) }
	default:
		return nil, usageError("standard input is not a terminal; pass --stdin to read the value from it")
	}
	value, err := readUnlessCancelled(ctx, read)
	if ctx.Err() != nil {
		return nil, &store.Error{Kind: store.KindCancelled, Message: "cancelled before the value was read"}
	}
	if err != nil {
		return nil, usageError("cannot read the value: " + err.Error())
	}
	return value, nil
}

// readUnlessCancelled returns what read returns, or nothing once ctx is
// done: a read of a terminal or pipe does not end when the process is
// interrupted.
func readUnlessCancelled(ctx context.Context, read func() ([]byte, error)) ([]byte, error) {
	type outcome struct {
		value []byte
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := read()
		done <- outcome{value, err}
	}()
	select {
	case o := <-done:
		return o.value, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func renameCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "rename <namespace>/<name> <new-namespace>/<new-name>",
		SuggestFor: []string{"mv", "move"},
		Short:      "Rename an entry or move it to another namespace",
		Long: `Rename an entry, move it to another namespace, or both, in one write. The
value, kind and notes stay. Fails if the new entry already exists.`,
		Example: `  mk rename github/TOKEN github/GITHUB_TOKEN
  mk rename work/NPM_TOKEN npm/NPM_TOKEN`,
		DisableFlagsInUseLine: true,
		Args:                  entryArgs("rename takes the entry and its new reference, such as github/TOKEN github/GITHUB_TOKEN", 2),
		ValidArgsFunction:     completeEntries(env),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, _ := parseEntry(args[0])
			to, _ := parseEntry(args[1])
			if from == to {
				// Edit takes an unchanged entry as done without looking for it.
				return usageError("the new reference is the entry's own")
			}
			if err := env.Store.Edit(cmd.Context(), from, to, nil, nil); err != nil {
				return err
			}
			if jsonOutput(cmd) {
				writeJSON(env.Stdout, map[string]any{"from": from, "to": to})
			} else {
				fmt.Fprintln(env.Stdout, "renamed", from, "to", to)
			}
			return nil
		},
	}
	addJSONFlag(cmd, "the result")
	return cmd
}

func deleteCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:        "delete [--yes] <namespace>/<name>",
		SuggestFor: []string{"rm", "remove"},
		Short:      "Delete an entry",
		Long: `Delete an entry from the Keychain of this Mac and, through iCloud Keychain,
of every Mac on the same Apple Account. This cannot be undone. In a terminal
mk asks first; elsewhere pass --yes.`,
		Example: `  mk delete github/GITHUB_TOKEN
  mk delete --yes github/GITHUB_TOKEN`,
		DisableFlagsInUseLine: true,
		Args:                  entryArgs("delete takes one entry, such as github/GITHUB_TOKEN", 1),
		ValidArgsFunction:     completeEntries(env),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, _ := parseEntry(args[0])
			if yes, _ := cmd.Flags().GetBool("yes"); !yes {
				if err := confirmDelete(cmd.Context(), env, ref); err != nil {
					return err
				}
			}
			if err := env.Store.Delete(cmd.Context(), ref); err != nil {
				return err
			}
			if jsonOutput(cmd) {
				writeJSON(env.Stdout, map[string]any{"namespace": ref.Namespace, "name": ref.Name, "deleted": true})
			} else {
				fmt.Fprintln(env.Stdout, "deleted", ref)
			}
			return nil
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "delete without asking")
	addJSONFlag(cmd, "the result")
	return cmd
}

func confirmDelete(ctx context.Context, env *Env, ref store.Ref) error {
	if !term.IsTerminal(int(env.Stdin.Fd())) {
		return usageError("standard input is not a terminal; pass --yes to delete without asking")
	}
	fmt.Fprintf(env.Stderr, "delete %s from every Mac on this Apple Account? [y/N] ", ref)
	answer, _ := readUnlessCancelled(ctx, func() ([]byte, error) { return bufio.NewReader(env.Stdin).ReadBytes('\n') })
	if ctx.Err() == nil {
		switch strings.ToLower(strings.TrimSpace(string(answer))) {
		case "y", "yes":
			return nil
		}
	}
	return &store.Error{Kind: store.KindCancelled, Message: "not deleted"}
}

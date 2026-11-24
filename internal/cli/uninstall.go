package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/store"
)

func uninstallCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall [--yes] [--json]",
		Short: "Remove what Maskey installed on this Mac outside its app",
		Long: `Stop the Maskey service and remove its configuration, the activity
record, the window's saved state, and the mk and skill links. Quit Maskey
first.

mk lists what it will remove and asks once. Type y or yes and press
Return to confirm, ignoring case. Without terminal stdin, --yes is
required. Use it only when the owner asked to uninstall Maskey.

A Bitwarden store goes first, with its access token and its entries'
program access, which are in this Mac's Keychain: if one cannot be
removed, or the store configuration cannot be read, uninstall stops there,
so that a later uninstall still finds it.

Entries stay in iCloud Keychain on every Mac, and in Bitwarden: delete them
with mk delete first if they are no longer wanted. Maskey.app stays too; move it
to the Trash afterwards, or run brew uninstall --cask maskey.

Print one row per item: removed, absent, kept (a file, link, or service
that is not this Maskey's, or the service's files and the activity record
while the service may still be loaded), or failed. A failed item does not
stop the others; resolve it, then run uninstall again.`,
		DisableFlagsInUseLine: true,
		Args:                  noArgs("uninstall takes no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return uninstall(cmd, env)
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "Uninstall without asking")
	addJSONFlag(cmd, "Write the report or errors as JSON")
	return cmd
}

// removal is one item Maskey installs on this Mac, and what uninstall did
// with it.
type removal struct {
	Item   string `json:"item"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	// remove removes the item; it is nil for an item uninstall leaves, whose
	// Status is set when it is listed.
	remove func() error
}

// uninstall removes everything Maskey writes outside its app bundle and the
// Keychain, and what MyGo writes for the window. TestUninstall, and
// TestRemoveServiceUndoesInstall for the Maskey service, write each item
// where Maskey does and fail on what is left behind.
func uninstall(cmd *cobra.Command, env *Env) error {
	ctx := cmd.Context()
	if env.Home == "" {
		return withHint(&store.Error{Kind: store.KindPrerequisite, Message: "HOME is not set, so mk cannot find what Maskey installed"},
			"set HOME to your home directory, then run uninstall again")
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !term.IsTerminal(int(env.Stdin.Fd())) {
		return withHint(usageError("standard input is not a terminal and uninstall needs --yes"),
			"use --yes only when the owner asked to uninstall Maskey")
	}
	remote, err := remoteStores(ctx, env)
	if err != nil {
		return err
	}
	if !yes {
		var prompt strings.Builder
		for _, id := range remote {
			fmt.Fprintf(&prompt, "  %s\n", storeItem(id))
		}
		service := env.ServiceItems(ctx, env.Home)
		for _, item := range service {
			if item.Status == "" {
				fmt.Fprintf(&prompt, "  %s\n", item.Item)
			}
		}
		for _, r := range removals(env, service) {
			if r.remove != nil {
				fmt.Fprintf(&prompt, "  %s\n", r.Item)
			}
		}
		if prompt.Len() > 0 {
			prompt.WriteString("Entries stay in their stores, and Maskey.app stays where it is. Continue? [y/N] ")
			if err := confirm(ctx, env, "Remove what Maskey installed on this Mac:\n"+prompt.String(), "nothing was removed"); err != nil {
				return err
			}
		}
	}
	// What goes is decided again now: while the owner answers, mk setup can
	// reinstall the service or a link can come to point at another copy.
	var items []removal
	// Only the service removes a store's Keychain items, so they go before
	// it does, and a store that stays keeps the configuration that names it.
	if remote, err = remoteStores(ctx, env); err != nil {
		return err
	}
	for i, id := range remote {
		if err := env.ChangeStore(ctx, agent.OpRemoveStore, id); err != nil {
			removed := "nothing was removed"
			if i > 0 {
				removed = "only " + count(i, "store was", "stores were") + " removed"
			}
			return withHint(err, removed+"; run mk setup if the Maskey service is not running, then run uninstall again")
		}
		items = append(items, removal{Item: storeItem(id), Status: "removed"})
	}
	service := env.RemoveService(ctx, env.Home)
	for _, item := range service {
		items = append(items, removal{Item: item.Item, Status: item.Status, Detail: item.Detail})
	}
	for _, r := range removals(env, service) {
		if r.remove != nil {
			r.Status = "removed"
			if err := r.remove(); err != nil {
				r.Status, r.Detail = "failed", err.Error()
			}
		}
		items = append(items, r)
	}

	bundle := filepath.Dir(filepath.Dir(filepath.Dir(env.Executable)))
	if jsonOutput(cmd) {
		err := writeJSON(env.Stdout, struct {
			Items  []removal `json:"items"`
			Bundle string    `json:"bundle"`
		}{items, bundle})
		if err != nil {
			return err
		}
	} else {
		var text strings.Builder
		for _, r := range items {
			fmt.Fprintf(&text, "%-8s %s\n", r.Status, r.Item)
			if r.Detail != "" {
				fmt.Fprintf(&text, "%9s%s\n", "", r.Detail)
			}
		}
		fmt.Fprintf(&text, "Maskey.app is still at %s: move it to the Trash, or run brew uninstall --cask maskey if Homebrew installed it.\n", bundle)
		if _, err := io.WriteString(env.Stdout, text.String()); err != nil {
			return outputError()
		}
	}
	failed := 0
	for _, r := range items {
		if r.Status == "failed" {
			failed++
		}
	}
	if failed > 0 {
		// The mk link may be gone by now.
		return withHint(&store.Error{Kind: store.KindPrerequisite, Message: count(failed, "item was", "items were") + " not removed"},
			"resolve the failed items, then run "+env.Executable+" uninstall again")
	}
	return nil
}

// remoteStores names this Mac's stores besides iCloud Keychain. Uninstall
// stops at a configuration that cannot be read: removing it would leave its
// stores' access tokens in the Keychain with nothing that names them.
func remoteStores(ctx context.Context, env *Env) ([]string, error) {
	config, err := env.Stores(ctx)
	if err != nil {
		return nil, withHint(&store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration cannot be read, so mk cannot tell which stores to remove"},
			"nothing was removed; repair ~/Library/Application Support/"+store.Service+"/stores.json, or delete it and revoke its stores' access tokens in Bitwarden, since uninstall then leaves them in this Mac's Keychain")
	}
	ids := make([]string, len(config.Stores))
	for i, connection := range config.Remote() {
		ids[i] = connection.ID
	}
	return ids, nil
}

func storeItem(id string) string {
	return "store " + id + ", with its access token and program access"
}

// removals are the items besides the Maskey service, given its rows,
// each with what removing it would do now.
func removals(env *Env, service []agent.ServiceItem) []removal {
	activity := removePath(env.Activity.Dir(), env.Activity.Remove)
	// The service writes the activity record, so the record stays while the
	// service may still be loaded, as the service's own files do.
	if job := service[0].Status; activity.remove != nil && (job == "kept" || job == "failed") {
		activity = removal{Item: activity.Item, Status: "kept", Detail: "the Maskey service may still be loaded"}
	}
	userData := filepath.Join(env.Home, "Library", "Application Support", env.App)
	items := []removal{
		activity,
		removePath(userData, func() error { return os.RemoveAll(userData) }),
	}
	for _, link := range skillLinks(env.Home) {
		items = append(items, removeLink(link, skillSource(env.Executable)))
	}
	return append(items, removeLink(cliLink(env.Home), env.Executable))
}

// removePath is the removal of path with remove, unless path is absent.
func removePath(path string, remove func() error) removal {
	r := removal{Item: path}
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		r.Status = "absent"
	} else {
		r.remove = remove
	}
	return r
}

// removeLink is the removal of link only when it links to target, so that
// a file of the user's, or a link to another copy of Maskey, stays.
func removeLink(link, target string) removal {
	r := removal{Item: link}
	destination, err := os.Readlink(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.Status = "absent"
	case errors.Is(err, syscall.EINVAL):
		r.Status, r.Detail = "kept", "not a link"
	case err != nil:
		r.Status, r.Detail = "failed", err.Error()
	case destination != target:
		r.Status, r.Detail = "kept", "links elsewhere"
	default:
		r.remove = func() error { return os.Remove(link) }
	}
	return r
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/bitwarden"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

// StoreAccess is one store as the commands on entries use it.
type StoreAccess struct {
	Connection stores.Connection
	Store      *store.Store
	// ChangeAccess grants or revokes program access to refs once the owner
	// approves, as Store.ChangeAccess does.
	ChangeAccess func(ctx context.Context, refs []store.Ref, granted bool) ([]store.AccessChange, string, error)
}

const storeFlag = "store"

// addStoreFlag gives cmd, which works on entries, the choice of a store.
func addStoreFlag(cmd *cobra.Command, env *Env) {
	cmd.Flags().String(storeFlag, "", "Use the store with this `<name>` instead of the default")
	_ = cmd.RegisterFlagCompletionFunc(storeFlag, func(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		var names []cobra.Completion
		if config, err := env.Stores(cmd.Context()); err == nil {
			for _, connection := range config.All() {
				names = append(names, connection.ID)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	})
}

func storeName(cmd *cobra.Command, emptyNameHint string) (string, error) {
	name, _ := cmd.Flags().GetString(storeFlag)
	if name == "" && cmd.Flags().Changed(storeFlag) {
		return "", withHint(usageError("--store needs a name"), emptyNameHint)
	}
	return name, nil
}

// useStore makes the store cmd names with --store, or else this Mac's
// default, the one entry commands work on.
func useStore(cmd *cobra.Command, env *Env) error {
	name, err := storeName(cmd, "give --store a name from mk store list")
	if err != nil {
		return err
	}
	access, err := env.OpenStore(cmd.Context(), name)
	if store.IsKind(err, store.KindNotFound) {
		return withHint(err, listStoresHint)
	}
	if err != nil {
		return err
	}
	env.store, env.changeAccess, env.connection = access.Store, access.ChangeAccess, access.Connection
	if name != "" {
		env.storeArg = " --" + storeFlag + " " + access.Connection.ID
	}
	return nil
}

// remotePlace says where the entries of a store other than iCloud Keychain
// are, for a prompt about changing them there.
func remotePlace(connection stores.Connection) string {
	return "the Bitwarden project " + connection.ProjectName + " (store " + connection.ID + ")"
}

var entryCommands = regexp.MustCompile(`\bmk (list|run|set|get|grant|revoke|delete|rename)\b`)

// inStore gives the mk commands a hint names the --store the command was
// given, without which they would work on the default store.
func inStore(env *Env, hint string) string {
	if env.storeArg != "" && strings.HasPrefix(hint, "usage: ") {
		return strings.ReplaceAll(hint, "[--store <name>]", strings.TrimPrefix(env.storeArg, " "))
	}
	return entryCommands.ReplaceAllString(hint, "${0}"+env.storeArg)
}

const listStoresHint = "run mk store list to see this Mac's stores"

// typeNames are the stores' types as people know them.
var typeNames = map[string]string{
	stores.ICloud:    "iCloud Keychain",
	stores.Bitwarden: "Bitwarden Secrets Manager",
}

type storeRow struct {
	stores.Connection
	Default bool `json:"default"`
}

type storeResult struct {
	Store      string             `json:"store"`
	Action     string             `json:"action"`
	Connection *stores.Connection `json:"connection,omitempty"`
}

func storeCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store <command> [flags]",
		Short: "Manage this Mac's stores",
		Long: `A store holds entries. iCloud Keychain is built in and is named icloud.
A Bitwarden Secrets Manager project can be added as another store, which
two Macs with different Apple Accounts can share.

Each store has a name, which --store takes, and a type, which says what
it connects to. Entry commands that name no store use this Mac's default.
An entry command works on one store: nothing is looked up in a second one.

Stores, the default, and program access to a Bitwarden store's entries
belong to this Mac and do not sync.`,
		DisableFlagsInUseLine: true,
	}
	groupOfCommands(cmd)
	cmd.AddCommand(storeListCommand(env), storeAddCommand(env), storeTokenCommand(env),
		storeChangeCommand(env, "default", agent.OpDefaultStore, "Make a store the default", `Make the store the one that commands use when they name none with
--store. This changes only this Mac.`, "default_changed", "now the default"),
		storeChangeCommand(env, "sync", agent.OpSyncStore, "Read a Bitwarden store from Bitwarden now", `Read the store's project from Bitwarden now. mk otherwise uses what it
read within the last 5 minutes, and what it read last while Bitwarden
cannot be reached.`, "synced", "synced"),
		storeRemoveCommand(env))
	return cmd
}

func storeListCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [--json]",
		Short: "List this Mac's stores",
		Long: `List each store's name, its type, whether it is the default, and what
it connects to. JSON is {"stores":[...]} with id, type, default and, for a
Bitwarden store, region, organization, project, and project_name.`,
		DisableFlagsInUseLine: true,
		Args:                  noArgs("store list takes no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := env.Stores(cmd.Context())
			if err != nil {
				return err
			}
			connections := config.All()
			if jsonOutput(cmd) {
				rows := make([]storeRow, len(connections))
				for i, connection := range connections {
					rows[i] = storeRow{connection, connection.ID == config.Default}
				}
				return writeJSON(env.Stdout, map[string]any{"stores": rows})
			}
			text := make([]string, len(connections))
			for i, connection := range connections {
				isDefault, target := "", "this Apple Account"
				if connection.ID == config.Default {
					isDefault = "yes"
				}
				if connection.Type == stores.Bitwarden {
					target = strings.ToUpper(connection.Region) + " / " + connection.ProjectName
				}
				text[i] = strings.Join([]string{connection.ID, typeNames[connection.Type], isDefault, target}, "\t")
			}
			return writeTable(env, "NAME\tTYPE\tDEFAULT\tTARGET", text)
		},
	}
	addJSONFlag(cmd, "Write stores or errors as JSON")
	return cmd
}

func storeAddCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <type> [--name <name>] [--region <us|eu>] [--project <id|name>] [--stdin] [--json]",
		Short: "Connect a Bitwarden Secrets Manager project",
		Long: `Connect one Bitwarden Secrets Manager project as a store. The only
supported store type is bitwarden. Create a machine account in Bitwarden,
give it read and write access to the project, and create an access token
for it. Type the token at mk's hidden prompt with terminal stdin, or pipe
it with --stdin. Give each Mac a machine account of its own, so that
removing one Mac's access leaves the other's.

mk signs in with the token before it keeps anything. The token then
stays in this Mac's Keychain, in an item that does not sync. The store is
named bitwarden unless --name says otherwise; a second connection needs
another name. A machine account that can read several projects needs
--project. Adding a store leaves the default as it is.

An entry <namespace>/<NAME> is the Bitwarden secret whose name is
<namespace>/<NAME>#<kind>, such as github/GITHUB_TOKEN#token. Secrets with
other names are not entries.`,
		Example: `  mk store add bitwarden
  mk store add bitwarden --name work --region eu --project Deploy
  mk store default bitwarden`,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("store add takes the type bitwarden", 1, 1),
		ValidArgs:             []cobra.Completion{stores.Bitwarden},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != stores.Bitwarden {
				return atArgument(usageError("the only store type to add is bitwarden"), 1)
			}
			name, _ := cmd.Flags().GetString("name")
			region, _ := cmd.Flags().GetString("region")
			project, _ := cmd.Flags().GetString("project")
			addition := stores.Addition{ID: name, Type: stores.Bitwarden, Region: region, Project: project}
			connection, err := addition.Connection()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			config, err := env.Stores(ctx)
			if err != nil {
				return err
			}
			if err := config.CheckNewName(connection.ID); err != nil {
				return addStoreError(err)
			}
			fromStdin, _ := cmd.Flags().GetBool("stdin")
			addition.Token, err = readStoreToken(cmd, env, fromStdin, "Access token (input hidden): ",
				secretInput{"access token", "no store was added", "cancelled before the store was added"})
			if err != nil {
				return err
			}
			connection, err = env.AddStore(ctx, addition)
			if err != nil {
				return addStoreError(err)
			}
			return writeResult(cmd, env, storeResult{Store: connection.ID, Action: "added", Connection: &connection},
				fmt.Sprintf("%s: added, connected to the project %s\n", connection.ID, connection.ProjectName))
		},
	}
	cmd.Flags().String("name", "", "Name the store `<name>` instead of bitwarden")
	cmd.Flags().String("region", "", "Use Bitwarden's `<region>` cloud, us or eu (default us)")
	_ = cmd.RegisterFlagCompletionFunc("region", func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return slices.Sorted(maps.Keys(bitwarden.Regions)), cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().String("project", "", "Connect the project with this `<id|name>`")
	cmd.Flags().Bool("stdin", false, "Read the access token from standard input")
	addJSONFlag(cmd, "Write the store or errors as JSON")
	return cmd
}

func addStoreError(err error) error {
	switch store.KindOf(err) {
	case store.KindExists:
		return withHint(err, "choose another store name with --name")
	case store.KindNotFound:
		return withHint(err, "pass a project this machine account can read with --project")
	case store.KindConflict:
		return withHint(err, "pass the project ID with --project")
	case store.KindUnauthenticated:
		return withHint(err, "check the region and access token, then run the same command again")
	}
	return err
}

func storeTokenCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token [--stdin] [--json] <name>",
		Short: "Replace a store's access token",
		Long: `Replace the access token this Mac keeps for the store, as when the old
one expired or was revoked. Type the new token at mk's hidden prompt with
terminal stdin, or pipe it with --stdin. mk signs in with it and checks
that it reads the store's project before it replaces the old one. The
store keeps its name and its entries' program access.`,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("store token takes one store name", 1, 1),
		ValidArgsFunction:     completeStores(env, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := stores.CheckTokenStore(args[0]); err != nil {
				return err
			}
			ctx := cmd.Context()
			config, err := env.Stores(ctx)
			if err != nil {
				return err
			}
			connection, err := config.Named(args[0])
			if err != nil {
				return withHint(atArgument(err, 1), listStoresHint)
			}
			fromStdin, _ := cmd.Flags().GetBool("stdin")
			var target string
			if !fromStdin {
				target = connection.Target()
			}
			token, err := readStoreToken(cmd, env, fromStdin, fmt.Sprintf("New access token for %s (input hidden): ", connection.ID),
				secretInput{"access token", "the access token was not replaced", "cancelled before the access token was replaced"})
			if err != nil {
				return err
			}
			if err := env.SetToken(ctx, connection.ID, target, token); err != nil {
				return storeConnectionError(err)
			}
			return writeResult(cmd, env, storeResult{Store: connection.ID, Action: "token_replaced"}, connection.ID+": token replaced\n")
		},
	}
	cmd.Flags().Bool("stdin", false, "Read the access token from standard input")
	addJSONFlag(cmd, "Write the result or errors as JSON")
	return cmd
}

// completeStores completes the one store name a command takes, which is
// iCloud Keychain's only withICloud.
func completeStores(env *Env, withICloud bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		var names []cobra.Completion
		if config, err := env.Stores(cmd.Context()); err == nil && len(args) == 0 {
			connections := config.All()
			if !withICloud {
				connections = config.Remote()
			}
			for _, connection := range connections {
				names = append(names, connection.ID)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// storeChangeCommand is a command that changes the store its one argument
// names, through the Maskey service.
func storeChangeCommand(env *Env, name, op, short, long, action, done string) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   name + " [--json] <name>",
		Short:                 short,
		Long:                  long,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("store "+name+" takes one store name", 1, 1),
		ValidArgsFunction:     completeStores(env, op == agent.OpDefaultStore),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := env.ChangeStore(cmd.Context(), op, args[0])
			if op == agent.OpSyncStore {
				if hint := syncStoreRecovery(err); hint != "" {
					return withHint(err, hint)
				}
			}
			if store.IsKind(err, store.KindNotFound) {
				return withHint(atArgument(err, 1), listStoresHint)
			}
			if err != nil {
				return err
			}
			return writeResult(cmd, env, storeResult{Store: args[0], Action: action}, args[0]+": "+done+"\n")
		},
	}
	addJSONFlag(cmd, "Write the result or errors as JSON")
	return cmd
}

// syncStoreRecovery distinguishes a remote project that is no longer
// accessible from a store name this Mac does not know.
func syncStoreRecovery(err error) string {
	kind := store.KindOf(err)
	if kind == store.KindNotFound && !errors.Is(err, stores.ErrNoStore) || kind == store.KindPermissionDenied {
		return "check that the machine account can read the project in Bitwarden"
	}
	return ""
}

func storeRemoveCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove [--yes] [--json] <name>",
		Short: "Forget a store on this Mac",
		Long: `Remove the store from this Mac: its access token, the program access of
its entries, and what mk kept to read it offline. The entries stay in
Bitwarden. A removed default leaves icloud as the default.

Type y or yes and press Return to confirm, ignoring case. Without terminal
stdin, --yes is required. Use it only for a store the owner asked to remove.`,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("store remove takes one store name", 1, 1),
		ValidArgsFunction:     completeStores(env, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			yes, _ := cmd.Flags().GetBool("yes")
			if !yes && !term.IsTerminal(int(env.Stdin.Fd())) {
				return withHint(usageError("standard input is not a terminal and store remove needs --yes"),
					"use --yes only for the store the owner asked to remove")
			}
			ctx, id := cmd.Context(), args[0]
			var err error
			if yes {
				err = env.ChangeStore(ctx, agent.OpRemoveStore, id)
			} else {
				config, readErr := env.Stores(ctx)
				if readErr != nil {
					return readErr
				}
				connection, lookupErr := config.Removable(id)
				if lookupErr != nil {
					return storeConnectionError(lookupErr)
				}
				prompt := fmt.Sprintf("Remove the store %s from this Mac? Its access token, its entries' program access, and the saved copy will be removed. Entries stay in Bitwarden.", connection.ID)
				if config.Default == connection.ID {
					prompt += " Commands will use icloud by default."
				}
				if err := confirm(ctx, env, prompt+" Continue? [y/N] ", "no store was removed"); err != nil {
					return err
				}
				err = env.RemoveStore(ctx, connection)
			}
			if err != nil {
				return storeConnectionError(err)
			}
			return writeResult(cmd, env, storeResult{Store: id, Action: "removed"}, id+": removed\n")
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "Remove without asking")
	addJSONFlag(cmd, "Write the result or errors as JSON")
	return cmd
}

func storeConnectionError(err error) error {
	switch store.KindOf(err) {
	case store.KindNotFound:
		return withHint(atArgument(err, 1), listStoresHint)
	case store.KindConflict:
		return withHint(err, "run mk store list to check the store, then retry")
	}
	return err
}

func readStoreToken(cmd *cobra.Command, env *Env, fromStdin bool, prompt string, input secretInput) ([]byte, error) {
	if !fromStdin && !term.IsTerminal(int(env.Stdin.Fd())) {
		return nil, withHint(usageError("standard input is not a terminal and "+cmd.Parent().Name()+" "+cmd.Name()+" needs --stdin"),
			"have the owner run this command in their terminal or pipe the access token with --stdin")
	}
	return readInput(cmd.Context(), env, prompt, fromStdin, input)
}

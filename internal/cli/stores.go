package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Entwining/maskey/internal/agent"
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

// useStore makes the store cmd names with --store, or else this Mac's
// default, the one cmd works on. A command without the flag works on no
// store's entries.
func useStore(cmd *cobra.Command, env *Env) error {
	flag := cmd.Flags().Lookup(storeFlag)
	if flag == nil {
		return nil
	}
	access, err := env.OpenStore(cmd.Context(), flag.Value.String())
	if store.IsKind(err, store.KindNotFound) {
		return withHint(err, "run mk store list to see this Mac's stores")
	}
	if err != nil {
		return err
	}
	env.store, env.changeAccess, env.connection = access.Store, access.ChangeAccess, access.Connection
	if flag.Changed {
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
	return entryCommands.ReplaceAllString(hint, "${0}"+env.storeArg)
}

// typeNames are the stores' types as people know them.
var typeNames = map[string]string{
	stores.ICloud:    "iCloud Keychain",
	stores.Bitwarden: "Bitwarden Secrets Manager",
}

type storeRow struct {
	stores.Connection
	Default bool `json:"default"`
}

func storeCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store <command> [flags]",
		Short: "List, add, and remove this Mac's stores",
		Long: `A store holds entries. iCloud Keychain is built in and is named icloud.
A Bitwarden Secrets Manager project can be added as another store, which
two Macs with different Apple Accounts can share.

Each store has a name, which --store takes, and a type, which says what
it connects to. Commands that name no store use this Mac's default. A
command works on one store: nothing is looked up in a second one.

Stores, the default, and program access to a Bitwarden store's entries
belong to this Mac and do not sync.`,
		DisableFlagsInUseLine: true,
	}
	groupOfCommands(cmd)
	cmd.AddCommand(storeListCommand(env), storeAddCommand(env), storeTokenCommand(env),
		storeChangeCommand(env, "default", agent.OpDefaultStore, "Make a store the default", `Make the store the one that commands use when they name none with
--store. This changes only this Mac.`, "now the default"),
		storeChangeCommand(env, "sync", agent.OpSyncStore, "Read a Bitwarden store from Bitwarden now", `Read the store's project from Bitwarden now. mk otherwise uses what it
read within the last 5 minutes, and what it read last while Bitwarden
cannot be reached.`, "synced"),
		storeChangeCommand(env, "remove", agent.OpRemoveStore, "Forget a store on this Mac", `Remove the store from this Mac: its access token, the program access of
its entries, and what mk kept to read it offline. The entries stay in
Bitwarden. A removed default leaves icloud as the default.`, "removed"))
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
			var rows []storeRow
			var text []string
			for _, connection := range config.All() {
				row := storeRow{connection, connection.ID == config.Default}
				rows = append(rows, row)
				isDefault, target := "", "this Apple Account"
				if row.Default {
					isDefault = "yes"
				}
				if connection.Type == stores.Bitwarden {
					target = strings.ToUpper(connection.Region) + " / " + connection.ProjectName
				}
				text = append(text, strings.Join([]string{connection.ID, typeNames[connection.Type], isDefault, target}, "\t"))
			}
			if jsonOutput(cmd) {
				return writeJSON(env.Stdout, map[string]any{"stores": rows})
			}
			return writeTable(env, "NAME\tTYPE\tDEFAULT\tTARGET", text)
		},
	}
	addJSONFlag(cmd, "Write stores or errors as JSON")
	return cmd
}

func storeAddCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add bitwarden [--name <name>] [--region us|eu] [--project <id|name>] [--stdin] [--json]",
		Short: "Connect a Bitwarden Secrets Manager project",
		Long: `Connect one Bitwarden Secrets Manager project as a store. Create a machine
account in Bitwarden, give it read and write access to the project, and
create an access token for it. Type the token at mk's hidden prompt, or
pipe it with --stdin. Give each Mac a machine account of its own, so that
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
			fromStdin, _ := cmd.Flags().GetBool("stdin")
			token, err := readSecret(cmd.Context(), env, "Access token (input hidden): ", fromStdin)
			if err != nil {
				return err
			}
			connection, err := env.AddStore(cmd.Context(), stores.Addition{ID: name, Type: stores.Bitwarden, Region: region, Project: project, Token: token})
			if store.IsKind(err, store.KindExists) {
				return withHint(err, "pass another name with --name, or remove the store first with mk store remove")
			}
			if err != nil {
				return err
			}
			return writeResult(cmd, env, map[string]any{"store": connection},
				fmt.Sprintf("%s: added, connected to the project %s\n", connection.ID, connection.ProjectName))
		},
	}
	cmd.Flags().String("name", "", "Name the store `<name>` instead of bitwarden")
	cmd.Flags().String("region", "", "Use Bitwarden's `<region>` cloud, us or eu (default us)")
	cmd.Flags().String("project", "", "Connect the project with this `<id|name>`")
	cmd.Flags().Bool("stdin", false, "Read the access token from standard input")
	addJSONFlag(cmd, "Write the store or errors as JSON")
	return cmd
}

func storeTokenCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token [--stdin] [--json] <name>",
		Short: "Replace a store's access token",
		Long: `Replace the access token this Mac keeps for the store, as when the old
one expired or was revoked. Type the new token at mk's hidden prompt, or
pipe it with --stdin. mk signs in with it and checks that it reads the
store's project before it replaces the old one. The store keeps its name
and its entries' program access.`,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("store token takes one store name", 1, 1),
		ValidArgsFunction:     completeStores(env, false),
		RunE: func(cmd *cobra.Command, args []string) error {
			fromStdin, _ := cmd.Flags().GetBool("stdin")
			token, err := readSecret(cmd.Context(), env, fmt.Sprintf("New access token for %s (input hidden): ", args[0]), fromStdin)
			if err != nil {
				return err
			}
			err = env.SetToken(cmd.Context(), args[0], token)
			if store.IsKind(err, store.KindNotFound) {
				return withHint(atArgument(err, 1), "run mk store list to see this Mac's stores")
			}
			if err != nil {
				return err
			}
			return writeResult(cmd, env, map[string]any{"store": args[0], "action": "token replaced"}, args[0]+": token replaced\n")
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
func storeChangeCommand(env *Env, name, op, short, long, done string) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   name + " [--json] <name>",
		Short:                 short,
		Long:                  long,
		DisableFlagsInUseLine: true,
		Args:                  countArgs("store "+name+" takes one store name", 1, 1),
		ValidArgsFunction:     completeStores(env, op == agent.OpDefaultStore),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := env.ChangeStore(cmd.Context(), op, args[0])
			if store.IsKind(err, store.KindNotFound) {
				return withHint(atArgument(err, 1), "run mk store list to see this Mac's stores")
			}
			if err != nil {
				return err
			}
			return writeResult(cmd, env, map[string]any{"store": args[0], "action": done}, args[0]+": "+done+"\n")
		},
	}
	addJSONFlag(cmd, "Write the result or errors as JSON")
	return cmd
}

// readSecret reads a credential that is no entry's value from stdin, or
// from a hidden prompt on the terminal.
func readSecret(ctx context.Context, env *Env, prompt string, fromStdin bool) ([]byte, error) {
	if !fromStdin {
		in, out := prompts(env)
		return readHidden(ctx, in, out, prompt)
	}
	value, err := readUnlessCancelled(ctx, func() ([]byte, error) {
		value, err := io.ReadAll(env.Stdin)
		return withoutLineEnding(value), err
	})
	return value, readError(ctx, err)
}

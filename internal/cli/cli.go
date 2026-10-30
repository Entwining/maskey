// Package cli implements the mk command line.
//
// The commands' usage lines, help texts, output shapes, error kinds and exit
// statuses are a contract that agents and scripts rely on; the exit-codes
// help topic states the statuses.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Entwining/maskey/internal/runner"
	"github.com/Entwining/maskey/internal/store"
)

// Env is what a command runs against.
type Env struct {
	Store *store.Store
	// Check is the host check of the Keychain helper, and CheckService a
	// request to the Keychain service.
	Check        func(context.Context) error
	CheckService func(context.Context) error
	Setup        func(context.Context) error
	// Skill is the agent skill's SKILL.md, and SkillSource the directory in
	// the app bundle that holds it.
	Skill       []byte
	SkillSource string
	Home        string
	Stdin       *os.File
	Stdout      io.Writer
	Stderr      io.Writer
	Version     string
}

// Outcome is how the process ends: an exit code, or a signal to end by.
type Outcome struct {
	Code   int
	Signal syscall.Signal
}

// Kinds of failure the command line adds to the store's.
const (
	kindUsage         store.Kind = "usage"
	kindRunFailed     store.Kind = "run_failed"
	kindNotExecutable store.Kind = "command_not_executable"
	kindNotFound      store.Kind = "command_not_found"
)

var exitCodes = map[store.Kind]int{
	kindUsage:              2,
	store.KindInvalid:      2,
	store.KindNotFound:     3,
	store.KindExists:       4,
	store.KindConflict:     5,
	store.KindPrerequisite: 6,
	store.KindLocked:       7,
	store.KindKeychain:     8,
	store.KindUncertain:    9,
	store.KindUnavailable:  10,
	store.KindCancelled:    130,
	kindRunFailed:          runner.ExitFailure,
	kindNotExecutable:      runner.ExitNotExecutable,
	kindNotFound:           runner.ExitNotFound,
}

var recoveries = map[store.Kind]string{
	store.KindNotFound:     "run mk list to see the entries",
	store.KindExists:       "choose another name, or delete the existing entry first",
	store.KindConflict:     "check the entries with mk list, then retry",
	store.KindPrerequisite: "run mk doctor for what is missing",
	store.KindLocked:       "unlock this Mac, then retry",
	store.KindKeychain:     "retry; run mk doctor if it persists",
	store.KindUncertain:    "check the entry in Maskey before repeating the write; mk list cannot show whether a value was replaced",
	store.KindUnavailable:  "run mk outside the sandbox, or let the sandbox connect to the Unix socket ~/Library/Application Support/" + store.Service + "/keychain.sock; mk doctor shows whether the service runs",
	kindNotExecutable:      "check the file's permissions",
	kindNotFound:           "check the command name and PATH",
}

func init() {
	// Commands are listed in the order they are added, by use.
	cobra.EnableCommandSorting = false
}

func usageError(message string) error { return &store.Error{Kind: kindUsage, Message: message} }

// Run executes the command line args, without the program name.
func Run(ctx context.Context, args []string, env Env) Outcome {
	root := newRoot(&env)
	// A nil slice would make Cobra read os.Args.
	root.SetArgs(append([]string{}, args...))
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return Outcome{}
	}
	var outcome *exitError
	if errors.As(err, &outcome) {
		return outcome.outcome
	}
	return report(&env, cmd, err)
}

// exitError ends mk with the outcome of the command it ran.
type exitError struct{ outcome Outcome }

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.outcome.Code) }

// report prints err on stderr and returns its exit status. Messages come
// only from mk and the store, which never quote an argument that failed
// validation: a value put in the wrong place must not reach the output.
func report(env *Env, cmd *cobra.Command, err error) Outcome {
	var e *store.Error
	var unknown *unknownCommand
	switch {
	case errors.As(err, &unknown):
		e = &store.Error{Kind: kindUsage, Message: "unknown command"}
	case !errors.As(err, &e):
		e = &store.Error{Kind: kindUsage, Message: "the command line is wrong"}
	}
	recovery := recoveries[e.Kind]
	switch {
	case unknown != nil && len(unknown.suggestions) > 0:
		recovery = "did you mean " + cmd.CommandPath() + " " + unknown.suggestions[0] + "?"
	case e.Kind == kindUsage && cmd.HasAvailableSubCommands():
		recovery = "run " + cmd.CommandPath() + " --help for the commands"
	case e.Kind == kindUsage:
		recovery = "usage: " + cmd.UseLine()
	case e.Kind == store.KindNotFound && e.Ref != nil:
		recovery = "run mk list " + e.Ref.Namespace + " to see its entries"
	case e.Kind == store.KindConflict && cmd.Name() == "run":
		recovery = "select namespaces and entries whose names do not overlap"
	case e.Kind == store.KindExists && cmd.Name() == "install":
		recovery = "move it away, then run mk skill install again"
	}
	code := exitCodes[e.Kind]
	if cmd.Name() == "run" && code != runner.ExitNotExecutable && code != runner.ExitNotFound {
		// mk's own failures must not look like the command's statuses.
		code = runner.ExitFailure
	}
	if jsonOutput(cmd) {
		writeJSON(env.Stderr, map[string]any{"error": errorBody{e, recovery}})
	} else {
		fmt.Fprintf(env.Stderr, "mk: error[%s]: %s\n", e.Kind, e.Error())
		if recovery != "" {
			fmt.Fprintf(env.Stderr, "mk: hint: %s\n", recovery)
		}
	}
	return Outcome{Code: code}
}

type errorBody struct {
	*store.Error
	Recovery string `json:"recovery,omitempty"`
}

func jsonOutput(cmd *cobra.Command) bool {
	on, _ := cmd.Flags().GetBool("json")
	return on
}

func addJSONFlag(cmd *cobra.Command, what string) {
	cmd.Flags().Bool("json", false, "print "+what+" as JSON")
}

func writeJSON(w io.Writer, document any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(document)
}

// flagError rebuilds pflag's errors from the flags mk defines, because its
// messages quote the argument, which may be a value.
func flagError(_ *cobra.Command, err error) error {
	var notExist *pflag.NotExistError
	var required *pflag.ValueRequiredError
	var invalid *pflag.InvalidValueError
	switch {
	case errors.As(err, &notExist):
		return usageError("unknown flag")
	case errors.As(err, &required):
		return usageError("--" + required.GetFlag().Name + " needs a value")
	case errors.As(err, &invalid):
		return usageError("invalid value for --" + invalid.GetFlag().Name)
	}
	return usageError("bad flag syntax")
}

// parseEntry reads an entry argument, <namespace>/<name>.
func parseEntry(arg string) (store.Ref, error) {
	if strings.Contains(arg, "=") {
		return store.Ref{}, usageError("an entry is <namespace>/<name>; values are never arguments")
	}
	namespace, name, ok := strings.Cut(arg, "/")
	if !ok {
		return store.Ref{}, usageError("an entry is <namespace>/<name>, such as github/GITHUB_TOKEN")
	}
	ref := store.Ref{Namespace: namespace, Name: name}
	return ref, store.ValidateRef(ref)
}

func entryArgs(message string, n int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != n {
			return usageError(message)
		}
		for _, arg := range args {
			if _, err := parseEntry(arg); err != nil {
				return err
			}
		}
		return nil
	}
}

func newRoot(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use: "mk",
		Long: `mk keeps API keys and other secrets in iCloud Keychain with Maskey, and runs
commands with them as environment variables. No command prints a stored value.

An entry is <namespace>/<name>, such as github/GITHUB_TOKEN. Namespaces use
lowercase letters, digits, '.', '_' and '-', starting with a letter or digit.
Names are environment variable names: letters, digits and '_', not starting
with a digit.

Results go to stdout, as text or with --json as JSON; errors go to stderr.
"mk help exit-codes" lists the error kinds and exit statuses.`,
		Example: `  # Store a token; mk prompts for the value without echoing it
  mk set github/GITHUB_TOKEN

  # Store a value another program prints, without its trailing newline
  printf '%s' "$(gh auth token)" | mk set --stdin github/GITHUB_TOKEN

  # Run a command with every entry of a namespace in its environment
  mk run -n github -- gh auth status`,
		Version:               env.Version,
		DisableFlagsInUseLine: true,
		SilenceErrors:         true,
		SilenceUsage:          true,
	}
	groupOfCommands(root)
	root.Flags().Bool("version", false, "version for mk")
	root.SetFlagErrorFunc(flagError)
	root.AddGroup(
		&cobra.Group{ID: "core", Title: "Core Commands:"},
		&cobra.Group{ID: "setup", Title: "Setup Commands:"},
	)
	for _, cmd := range []*cobra.Command{runCommand(env), listCommand(env), setCommand(env), renameCommand(env), deleteCommand(env)} {
		cmd.GroupID = "core"
		root.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{doctorCommand(env), setupCommand(env), skillCommand(env)} {
		cmd.GroupID = "setup"
		root.AddCommand(cmd)
	}
	root.AddCommand(exitCodesTopic())
	root.SetHelpCommand(helpCommand())
	return root
}

// groupOfCommands makes cmd, which only holds subcommands, print its help
// when run alone and report an unknown subcommand. Cobra would print the
// help for an unknown subcommand and succeed, or, at the root, quote it.
func groupOfCommands(cmd *cobra.Command) {
	// SuggestionsFor uses Cobra's default distance only when set.
	cmd.SuggestionsMinimumDistance = 2
	cmd.Args = cobra.ArbitraryArgs
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		return &unknownCommand{suggestions: cmd.SuggestionsFor(args[0])}
	}
}

// unknownCommand is the usage error of an unknown command, with the
// commands its name resembles.
type unknownCommand struct{ suggestions []string }

func (e *unknownCommand) Error() string { return "unknown command" }

func helpCommand() *cobra.Command {
	return &cobra.Command{
		Use:                   "help [<command> | exit-codes]",
		Short:                 "Help about any command",
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, rest, err := cmd.Root().Find(args)
			if err != nil || len(rest) > 0 || len(args) > 0 && target == cmd.Root() {
				return usageError("unknown help topic")
			}
			// As Cobra's own help command does, so the help flag is listed.
			target.InitDefaultHelpFlag()
			return target.Help()
		},
	}
}

func exitCodesTopic() *cobra.Command {
	return &cobra.Command{
		Use:   "exit-codes",
		Short: "Exit statuses and error kinds",
		Long: `mk exits with a status for each kind of error. Errors on stderr name the
kind: "mk: error[<kind>]: ..." or, with --json, {"error":{"kind":...}}.

  0    success
  2    usage          the command line is wrong
  2    invalid        a namespace, name, or value breaks a rule
  3    not_found      the entry or namespace does not exist
  4    exists         the entry already exists
  5    conflict       the entry changed concurrently, or a name is selected
                      from two namespaces
  6    prerequisite   this Mac lacks something mk needs
  7    locked         the Keychain stays locked until this Mac is unlocked
  8    keychain       the Keychain returned another error
  9    uncertain      a write may or may not have happened: check the entry
                      before repeating it
  10   unavailable    the Keychain cannot be reached from here, as inside a
                      sandbox
  130  cancelled      interrupted, or not confirmed

mk run exits with its command's status, or ends by the same signal. When mk
itself fails it exits 125 with one of the kinds above or run_failed, 126
with command_not_executable, or 127 with command_not_found.`,
	}
}

func noArgs(message string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > 0 {
			return usageError(message)
		}
		return nil
	}
}

// completeNamespaces and completeEntries list names, never values; a
// failure completes nothing.
func completeNamespaces(env *Env) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		namespaces, err := env.Store.Namespaces(cmd.Context())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := make([]cobra.Completion, len(namespaces))
		for i, n := range namespaces {
			names[i] = n.Name
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeEntries completes the first argument, or an option's value, with entries.
func completeEntries(env *Env) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		_, refs, _, err := env.Store.Listing(cmd.Context())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		entries := make([]cobra.Completion, len(refs))
		for i, ref := range refs {
			entries[i] = ref.String()
		}
		return entries, cobra.ShellCompDirectiveNoFileComp
	}
}

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
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
)

// Env is what a command runs against.
type Env struct {
	Store *store.Store
	// ChangeAccess grants or revokes program access to refs once the owner
	// approves, as Store.ChangeAccess does.
	ChangeAccess func(ctx context.Context, refs []store.Ref, granted bool) ([]store.AccessChange, string, error)
	// Activity is this Mac's record of Keychain operations, which reports a
	// failure to record through the command.
	Activity *activity.Log
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

func init() {
	// Commands are listed in the order they are added, by use.
	cobra.EnableCommandSorting = false
}

// Run executes the command line args, without the program name.
func Run(ctx context.Context, args []string, env Env) Outcome {
	root := newRoot(&env)
	// A nil slice would make Cobra read os.Args.
	root.SetArgs(append([]string{}, args...))
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

func newRoot(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use: "mk [command] [flags]",
		Long: `Maskey stores credentials in iCloud Keychain. Each entry is <namespace>/<NAME>.
With mk run, github/GITHUB_TOKEN sets GITHUB_TOKEN in the child's environment.`,
		Example: `  mk set github/GITHUB_TOKEN
  gh auth token | mk set --stdin github/GITHUB_TOKEN
  mk run github -- gh auth status

Give values to mk set at its hidden prompt or with --stdin. Keep secret
values out of command-line arguments.`,
		Annotations: map[string]string{pointersAnnotation: `Use mk <command> --help for flags and examples.
Commands that support --json list it in their help; piping does not enable it.
See mk help exit-codes for error kinds and exit statuses.
See mk skill show for the agent workflow.`},
		Version:               env.Version,
		DisableFlagsInUseLine: true,
		SilenceErrors:         true,
		SilenceUsage:          true,
	}
	// Set before InitDefaultCompletionCmd, which keeps the writer it finds.
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	groupOfCommands(root)
	root.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		env.Activity.OnFailure(func(err error) { warn(cmd, env, "the activity record failed: "+err.Error()) })
	}
	root.Flags().Bool("version", false, "Show the installed version")
	root.SetFlagErrorFunc(flagError)
	root.SetHelpFunc(writeHelp)
	root.AddGroup(
		&cobra.Group{ID: "core", Title: "Core commands:"},
		&cobra.Group{ID: "setup", Title: "Setup commands:"},
	)
	for _, cmd := range []*cobra.Command{runCommand(env), listCommand(env), setCommand(env), getCommand(env), renameCommand(env), deleteCommand(env), grantCommand(env), revokeCommand(env), activityCommand(env)} {
		cmd.GroupID = "core"
		root.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{doctorCommand(env), setupCommand(env), skillCommand(env)} {
		cmd.GroupID = "setup"
		root.AddCommand(cmd)
	}
	root.AddCommand(exitCodesTopic())
	root.InitDefaultCompletionCmd()
	describeCompletion(root)
	root.SetHelpCommand(helpCommand())
	root.InitDefaultHelpCmd()
	visit(root, func(cmd *cobra.Command) {
		// Flags are listed in the order they are defined, after this one.
		cmd.Flags().SortFlags = false
		cmd.Flags().BoolP("help", "h", false, "Show help")
	})
	return root
}

func visit(cmd *cobra.Command, do func(*cobra.Command)) {
	do(cmd)
	for _, child := range cmd.Commands() {
		visit(child, do)
	}
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

func jsonOutput(cmd *cobra.Command) bool {
	on, _ := cmd.Flags().GetBool("json")
	return on
}

func addJSONFlag(cmd *cobra.Command, usage string) {
	cmd.Flags().Bool("json", false, usage)
}

func writeJSON(w io.Writer, document any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return outputError()
	}
	return nil
}

// warn reports on stderr a problem that leaves the command's result alone,
// as JSON with --json.
func warn(cmd *cobra.Command, env *Env, message string) {
	if jsonOutput(cmd) {
		_ = writeJSON(env.Stderr, map[string]any{"warning": map[string]string{"message": message}})
		return
	}
	fmt.Fprintln(env.Stderr, "mk: warning:", message)
}

func writeResult(cmd *cobra.Command, env *Env, document any, text string) error {
	if jsonOutput(cmd) {
		return writeJSON(env.Stdout, document)
	}
	if _, err := io.WriteString(env.Stdout, text); err != nil {
		return outputError()
	}
	return nil
}

func countArgs(message string, minimum, maximum int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < minimum || maximum >= 0 && len(args) > maximum {
			return usageError(message)
		}
		return nil
	}
}

func noArgs(message string) cobra.PositionalArgs { return countArgs(message, 0, 0) }

func parseEntry(arg string, position int) (store.Ref, error) {
	if strings.Contains(arg, "=") {
		return store.Ref{}, atArgument(usageError("values are never arguments"), position)
	}
	namespace, name, ok := strings.Cut(arg, "/")
	if !ok {
		return store.Ref{}, atArgument(usageError("not an entry of the form <namespace>/<NAME>"), position)
	}
	ref := store.Ref{Namespace: namespace, Name: name}
	return ref, atArgument(store.ValidateRef(ref), position)
}

func parseEntries(args []string) ([]store.Ref, error) {
	refs := make([]store.Ref, len(args))
	for i, arg := range args {
		ref, err := parseEntry(arg, i+1)
		if err != nil {
			return nil, err
		}
		refs[i] = ref
	}
	return refs, nil
}

// parseSelectors parses namespaces and entries, as mk run and mk activity
// take them.
func parseSelectors(args []string) ([]store.Ref, error) {
	selectors := make([]store.Ref, len(args))
	for i, arg := range args {
		var err error
		if strings.Contains(arg, "/") {
			selectors[i], err = parseEntry(arg, i+1)
		} else {
			selectors[i], err = store.Ref{Namespace: arg}, atArgument(store.ValidateNamespace(arg), i+1)
		}
		if err != nil {
			return nil, err
		}
	}
	return selectors, nil
}

// distinct rejects a batch that names one entry twice, whose second write
// would act on the result of the first.
func distinct(refs []store.Ref) error {
	for i, ref := range refs {
		if first := slices.Index(refs, ref); first < i {
			return withHint(atArgument(usageError(fmt.Sprintf("repeats argument %d", first+1)), i+1), "pass each entry once")
		}
	}
	return nil
}

// completeEntries completes up to limit operands, or any number when limit
// is negative, with entries not already given. Completion lists names,
// never values; a failure completes nothing.
func completeEntries(env *Env, limit int) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if limit >= 0 && len(args) >= limit {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return completeNames(cmd.Context(), env, args, false)
	}
}

// completeNames lists the entry references, and the namespaces with
// withNamespaces, that given does not hold.
func completeNames(ctx context.Context, env *Env, given []string, withNamespaces bool) ([]cobra.Completion, cobra.ShellCompDirective) {
	namespaces, refs, _, err := env.Store.Listing(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []cobra.Completion
	if withNamespaces {
		for _, n := range namespaces {
			names = append(names, n.Name)
		}
	}
	for _, ref := range refs {
		names = append(names, ref.String())
	}
	names = slices.DeleteFunc(names, func(name string) bool { return slices.Contains(given, name) })
	return names, cobra.ShellCompDirectiveNoFileComp
}

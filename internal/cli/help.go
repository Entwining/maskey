package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

// usageAnnotation holds a command's usage lines, one per line, when Use
// cannot state them: several forms, or a path other than the command's.
const usageAnnotation = "usage"

// examplesHeadingAnnotation replaces the Examples heading of a command whose
// examples are of another kind.
const examplesHeadingAnnotation = "examples-heading"

// pointersAnnotation holds the lines that end a page, pointing to other
// commands.
const pointersAnnotation = "pointers"

func usageLines(cmd *cobra.Command) []string {
	if lines, ok := cmd.Annotations[usageAnnotation]; ok {
		return strings.Split(lines, "\n")
	}
	return []string{cmd.UseLine()}
}

// Hidden commands are help topics, whose pages omit flags.
func writeHelp(cmd *cobra.Command, _ []string) {
	bold := boldOn(cmd.OutOrStdout())
	sections := []string{bold("Usage:") + "\n  " + strings.Join(usageLines(cmd), "\n  ")}
	if cmd.Long != "" {
		sections = append(sections, cmd.Long)
	}
	sections = append(sections, commandSections(cmd, bold)...)
	if cmd.Example != "" {
		heading := "Examples:"
		if h, ok := cmd.Annotations[examplesHeadingAnnotation]; ok {
			heading = h
		}
		sections = append(sections, bold(heading)+"\n"+cmd.Example)
	}
	if !cmd.Hidden {
		sections = append(sections, bold("Flags:")+"\n"+flagRows(cmd, bold))
	}
	if pointers, ok := cmd.Annotations[pointersAnnotation]; ok {
		sections = append(sections, pointers)
	}
	// Cobra gives a help function no way to report a failed write.
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(sections, "\n\n"))
}

// boldOn returns how help written to w marks its headings, command names
// and flag spellings: bold on a terminal, unless NO_COLOR is nonempty
// (https://no-color.org) or TERM is dumb.
func boldOn(w io.Writer) func(string) string {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return func(s string) string { return s }
	}
	return func(s string) string { return "\x1b[1m" + s + "\x1b[0m" }
}

// column is text followed by the spaces that pad it to width, counted
// before bold adds its invisible escapes.
func column(text string, width int, bold func(string) string) string {
	return bold(text) + strings.Repeat(" ", width-len(text))
}

// commandSections lists cmd's subcommands under their groups' titles, and
// the rest under Commands, or Additional commands after groups.
func commandSections(cmd *cobra.Command, bold func(string) string) []string {
	var listed []*cobra.Command
	width := 0
	for _, c := range cmd.Commands() {
		// Cobra counts its help command as unavailable, but the root lists it.
		if c.IsAvailableCommand() || !cmd.HasParent() && c.Name() == "help" {
			listed = append(listed, c)
			width = max(width, len(c.Name())+2)
		}
	}
	rows := func(group string) string {
		var b strings.Builder
		for _, c := range listed {
			if c.GroupID == group {
				fmt.Fprintf(&b, "\n  %s%s", column(c.Name(), width, bold), c.Short)
			}
		}
		return b.String()
	}
	var sections []string
	for _, group := range cmd.Groups() {
		sections = append(sections, bold(group.Title)+rows(group.ID))
	}
	if other := rows(""); other != "" {
		title := "Commands:"
		if len(sections) > 0 {
			title = "Additional commands:"
		}
		sections = append(sections, bold(title)+other)
	}
	return sections
}

// flagRows lists cmd's flags, help first and the rest in definition order,
// with their descriptions aligned two spaces after the longest flag. A flag
// without a shorthand is indented to line up with those that have one.
func flagRows(cmd *cobra.Command, bold func(string) string) string {
	type row struct{ indent, spelling, usage string }
	var rows []row
	add := func(f *pflag.Flag) {
		r := row{indent: "    ", spelling: "--" + f.Name}
		if f.Shorthand != "" {
			r.indent, r.spelling = "", "-"+f.Shorthand+", --"+f.Name
		}
		varname, usage := pflag.UnquoteUsage(f)
		if varname != "" {
			r.spelling += " " + varname
		}
		r.usage = usage
		rows = append(rows, r)
	}
	flags := cmd.LocalFlags()
	add(flags.Lookup("help"))
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Name != "help" && !f.Hidden {
			add(f)
		}
	})
	width := 0
	for _, r := range rows {
		width = max(width, len(r.indent+r.spelling)+2)
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = "  " + r.indent + column(r.spelling, width-len(r.indent), bold) + r.usage
	}
	return strings.Join(lines, "\n")
}

func helpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help",
		Short: "Show help for a command or topic",
		Long:  "Show command help or exit codes without accessing the Keychain.",
		Annotations: map[string]string{usageAnnotation: `mk help [<command> [<subcommand>]]
mk help exit-codes`},
		Example: `  mk help run
  mk help skill install
  mk help exit-codes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, rest, err := cmd.Root().Find(args)
			if err != nil || len(rest) > 0 || len(args) > 0 && target == cmd.Root() {
				return usageError("unknown help topic")
			}
			return target.Help()
		},
	}
}

func exitCodesTopic() *cobra.Command {
	return &cobra.Command{
		Use:         "exit-codes",
		Hidden:      true,
		Annotations: map[string]string{usageAnnotation: "mk help exit-codes"},
		Long: `Exit statuses and error kinds

  0    success
  1    internal_error          an unexpected internal failure
  2    usage                   the command line is wrong
  2    invalid                 a namespace, name, or value breaks a rule
  3    not_found               an entry or namespace does not exist
  4    exists                  an entry or installation path already exists
  5    conflict                concurrent change or conflicting selection
  6    prerequisite            this Mac lacks something mk needs
  7    locked                  unlock this Mac before retrying
  8    keychain                another Keychain failure
  9    uncertain               a write may have happened; check before retrying
  10   unavailable             the Keychain and its service cannot be reached
  11   not_granted             program access is not granted
  12   io                      reading input or writing output failed
  130  cancelled               interrupted or confirmation declined

mk run returns its child's status or ends by the same signal. For mk's own
errors, run uses 125 for the kinds above or run_failed, 126 for
command_not_executable, and 127 for command_not_found. A child can also
return those numbers; the exit status alone does not identify its source.

Errors go to stderr as mk: error[<kind>]: <message>, with mk: hint: <recovery>
when a next step applies. --json uses an error object with kind, message,
exit_code, and optional recovery, argument, entry, or numeric os_status.
Check kind, not message text. argument is a 1-based operand position,
excluding flags and command names; for run it counts selectors before --.
Unknown or invalid operands are never repeated. Typo hints suggest only;
they never apply a correction. A warning, such as a failure to record
activity, goes to stderr as mk: warning: <message>, or with --json as one
{"warning":{"message":...}} line, and changes neither output nor status.

set, delete, grant, and revoke validate all references and collect
required input before writing. Writes run in argument order and stop at
the first failure; earlier writes remain. A batch is not a transaction. A
write failure reports completed and not_attempted, even when either list
is empty. The failed argument is in neither list. Preflight failures
change nothing. Never replay an uncertain write without checking it in
Maskey; mk list --json shows program_access but not whether a value was
replaced.

In text mode, each confirmed write prints its result on stdout. With
--json, full success prints one {"results":[...]} document on stdout;
operation failure leaves stdout empty and prints the error on stderr. Its
completed items include namespace, name, and action; not_attempted
contains operand positions. grant and revoke report unchanged for an
entry already in the requested state. Their document, and their error
after a change, add auth when an entry changed: dialog when the owner
approved in a dialog, reused under an approval from the last 5 minutes.
An output failure may leave partial results on stdout; it does not undo
confirmed writes.

doctor keeps check failures in its stdout report and returns the first
failed interpreter or Keychain status. Service and skill warnings do not
fail it. get prints raw bytes without an added newline; run keeps masked
child output. Their --json flag changes mk errors only.`,
		Args: noArgs("exit-codes takes no arguments"),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
}

// describeCompletion replaces the texts of Cobra's completion commands and
// gives them mk's argument errors, which never quote an argument.
func describeCompletion(root *cobra.Command) {
	completion, _, err := root.Find([]string{"completion"})
	if err != nil || completion == root {
		return
	}
	completion.Use = "completion <shell>"
	completion.DisableFlagsInUseLine = true
	completion.Short = "Generate shell completion"
	completion.Long = `Generate a shell completion script on standard output.

Generation does not install the script or read Keychain entries. Using
completion can list namespaces and entry references, never values. If the
Keychain cannot be reached, entry completion returns no candidates.`
	completion.Annotations = map[string]string{pointersAnnotation: "Use mk completion <shell> --help for loading instructions."}
	groupOfCommands(completion)
	completion.AddGroup(&cobra.Group{ID: "shells", Title: "Shells:"})
	shells := map[string]struct{ title, load string }{
		"bash":       {"Bash", "Requires bash-completion. Load for this shell session:\n  source <(mk completion bash)"},
		"zsh":        {"Zsh", "Enable completion and load for this shell session:\n  autoload -Uz compinit && compinit\n  source <(mk completion zsh)"},
		"fish":       {"Fish", "Load for this shell session:\n  mk completion fish | source"},
		"powershell": {"PowerShell", "Load for this shell session:\n  mk completion powershell | Out-String | Invoke-Expression"},
	}
	for _, shell := range completion.Commands() {
		name, text := shell.Name(), shells[shell.Name()]
		shell.Use = name + " [--no-descriptions]"
		shell.DisableFlagsInUseLine = true
		shell.GroupID = "shells"
		shell.Short = text.title + " completion"
		shell.Long = "Generate " + text.title + " completion on standard output.\n\n" + text.load
		shell.Args = noArgs("completion " + name + " takes no arguments")
		if f := shell.Flags().Lookup("no-descriptions"); f != nil {
			f.Usage = "Omit completion descriptions"
		}
	}
}

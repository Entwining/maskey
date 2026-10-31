package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Entwining/maskey/internal/runner"
	"github.com/Entwining/maskey/internal/store"
)

// Kinds of failure the command line adds to the store's.
const (
	kindUsage         store.Kind = "usage"
	kindIO            store.Kind = "io"
	kindInternal      store.Kind = "internal_error"
	kindRunFailed     store.Kind = "run_failed"
	kindNotExecutable store.Kind = "command_not_executable"
	kindNotFound      store.Kind = "command_not_found"
)

var exitCodes = map[store.Kind]int{
	kindInternal:           1,
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
	store.KindNotGranted:   11,
	kindIO:                 12,
	store.KindCancelled:    130,
	kindRunFailed:          runner.ExitFailure,
	kindNotExecutable:      runner.ExitNotExecutable,
	kindNotFound:           runner.ExitNotFound,
}

var recoveries = map[store.Kind]string{
	store.KindNotFound:     "run mk list to see the entries",
	store.KindExists:       "choose a different destination for mk rename",
	store.KindConflict:     "check the entries with mk list, then retry",
	store.KindPrerequisite: "run mk doctor for what is missing",
	store.KindLocked:       "unlock this Mac, then retry",
	store.KindKeychain:     "retry; run mk doctor if it persists",
	store.KindUncertain:    "check the entry in Maskey before repeating the write; mk list cannot show whether a value was replaced",
	store.KindUnavailable:  sandboxRecovery + "; mk doctor shows whether the service runs",
	kindIO:                 "check the output destination before continuing",
	kindNotExecutable:      "check the file's permissions",
	kindNotFound:           "check the command name and PATH",
}

const sandboxRecovery = "run mk outside the sandbox, or let the sandbox connect to the Unix socket ~/Library/Application Support/" + store.Service + "/keychain.sock"

// doctorRecoveries replace, in mk doctor's rows, the recoveries that send
// the user to mk doctor. A failed prerequisite's message says what is
// missing.
var doctorRecoveries = map[store.Kind]string{
	store.KindPrerequisite: "",
	store.KindKeychain:     "retry",
	store.KindUnavailable:  sandboxRecovery,
}

func usageError(message string) error { return &store.Error{Kind: kindUsage, Message: message} }

func outputError() error {
	return &store.Error{Kind: kindIO, Message: "cannot finish writing the result"}
}

func atArgument(err error, position int) error {
	var e *store.Error
	if !errors.As(err, &e) {
		return err
	}
	placed := *e
	placed.Argument = position
	return &placed
}

// hinted is an error whose recovery its kind's default does not cover.
type hinted struct {
	error
	hint string
}

func (e *hinted) Unwrap() error { return e.error }

func withHint(err error, hint string) error { return &hinted{err, hint} }

// unknownCommand is the usage error of an unknown command, with the
// commands its name resembles.
type unknownCommand struct{ suggestions []string }

func (e *unknownCommand) Error() string { return "unknown command" }

// partial is the failure of a batch after its first write was attempted,
// with the writes it confirmed and the positions of those it did not try.
type partial struct {
	error
	progress
}

type progress struct {
	Completed    []result `json:"completed"`
	NotAttempted []int    `json:"not_attempted"`
	// Auth is how the owner approved the completed changes to program
	// access.
	Auth string `json:"auth,omitempty"`
}

func (e *partial) Unwrap() error { return e.error }

// stopped is the failure err of a batch of total writes that confirmed
// completed and did not try those from index next on.
func stopped(err error, completed []result, next, total int) *partial {
	notAttempted := []int{}
	for i := next; i < total; i++ {
		notAttempted = append(notAttempted, i+1)
	}
	return &partial{err, progress{Completed: completed, NotAttempted: notAttempted}}
}

type errorBody struct {
	*store.Error
	ExitCode int `json:"exit_code"`
	*progress
	Recovery string `json:"recovery,omitempty"`
}

// report prints err on stderr and returns its exit status. Messages come
// only from mk and the store, which never quote an argument that failed
// validation or an entry they did not find: a value put in the wrong place
// must not reach the output.
func report(env *Env, cmd *cobra.Command, err error) Outcome {
	var e *store.Error
	var unknown *unknownCommand
	switch {
	case errors.As(err, &unknown):
		e = &store.Error{Kind: kindUsage, Message: "unknown command"}
	case !errors.As(err, &e):
		// Cobra's own usage errors are rebuilt by flagError and the
		// commands' argument checks, so anything else is mk's fault.
		e = &store.Error{Kind: kindInternal, Message: "an internal error prevented completion"}
	}
	code := exitCodes[e.Kind]
	if cmd.Name() == "run" && code != runner.ExitNotExecutable && code != runner.ExitNotFound {
		// Use the run-specific failure status; children can return it too,
		// so the status alone does not identify the source.
		code = runner.ExitFailure
	}
	body := &errorBody{Error: e, ExitCode: code, Recovery: recovery(env, cmd, err, e)}
	var batch *partial
	if errors.As(err, &batch) {
		body.progress = &batch.progress
	}
	if jsonOutput(cmd) {
		_ = writeJSON(env.Stderr, map[string]any{"error": body})
		return Outcome{Code: code}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "mk: error[%s]: %s\n", e.Kind, e.Error())
	if body.progress != nil {
		if len(body.Completed) == 0 {
			text.WriteString("mk: completed: none\n")
		}
		for i, r := range body.Completed {
			fmt.Fprintf(&text, "mk: completed: argument %d %s (%s)\n", i+1, r.Ref, r.Action)
		}
		fmt.Fprintf(&text, "mk: not attempted: %s\n", positions(body.NotAttempted))
	}
	if body.Recovery != "" {
		fmt.Fprintf(&text, "mk: hint: %s\n", body.Recovery)
	}
	_, _ = fmt.Fprint(env.Stderr, text.String())
	return Outcome{Code: code}
}

func positions(list []int) string {
	if len(list) == 0 {
		return "none"
	}
	text := make([]string, len(list))
	for i, n := range list {
		text[i] = fmt.Sprint(n)
	}
	if len(list) == 1 {
		return "argument " + text[0]
	}
	return "arguments " + strings.Join(text, ", ")
}

func recovery(env *Env, cmd *cobra.Command, err error, e *store.Error) string {
	var unknown *unknownCommand
	var hint *hinted
	switch {
	case errors.As(err, &unknown) && len(unknown.suggestions) > 0:
		return "did you mean " + cmd.CommandPath() + " " + unknown.suggestions[0] + "?"
	case errors.As(err, &hint):
		return hint.hint
	case e.Kind == kindUsage && cmd.HasAvailableSubCommands():
		return "run " + cmd.CommandPath() + " --help for the commands"
	case e.Kind == kindUsage:
		return "usage: " + strings.Join(usageLines(cmd), " or ")
	// A batch writes only entries its check found by their exact names.
	case e.Kind == store.KindNotFound && e.Argument > 0 && !errors.As(err, new(*partial)):
		if similar := similarName(env, cmd, e.Argument); similar != "" {
			return "did you mean " + similar + "?"
		}
	case e.Kind == store.KindUncertain && (cmd.Name() == "grant" || cmd.Name() == "revoke"):
		return "check program_access in mk list --json before repeating the change"
	case e.Kind == store.KindNotGranted && e.Ref != nil:
		return "use mk run " + e.Ref.String() + " -- <command>, or ask the owner to grant program access"
	case e.Kind == store.KindConflict && cmd.Name() == "run":
		return "select only the entry you intend for that NAME, as <namespace>/<NAME>"
	case e.Kind == store.KindExists && cmd.Name() == "install":
		return "move it away, then run mk skill install again"
	}
	return recoveries[e.Kind]
}

// flagError rebuilds pflag's errors from the flags mk defines, because its
// messages quote the argument, which may be a value.
func flagError(cmd *cobra.Command, err error) error {
	var notExist *pflag.NotExistError
	var invalid *pflag.InvalidValueError
	switch {
	case errors.As(err, &notExist) && cmd.Name() == "run" && isSelectorFlag(notExist.GetSpecifiedName()):
		return selectorFlagError()
	case errors.As(err, &notExist):
		err = usageError("unknown flag")
	case errors.As(err, &invalid):
		err = usageError("invalid value for --" + invalid.GetFlag().Name)
	default:
		err = usageError("bad flag syntax")
	}
	return withHint(err, "run "+cmd.CommandPath()+" --help for its flags")
}

// similarName is the one existing entry or namespace, as the operand at
// position names one or the other, that differs from that operand only in
// case, or else within two edits of it. It only suggests: mk never acts on
// a guess.
func similarName(env *Env, cmd *cobra.Command, position int) string {
	operands := cmd.Flags().Args()
	if position > len(operands) {
		return ""
	}
	operand := operands[position-1]
	namespaces, refs, _, err := env.Store.Listing(cmd.Context())
	if err != nil {
		return ""
	}
	var names []string
	if strings.Contains(operand, "/") {
		for _, ref := range refs {
			names = append(names, ref.String())
		}
	} else {
		for _, n := range namespaces {
			names = append(names, n.Name)
		}
	}
	only := func(similar func(name string) bool) string {
		found := ""
		for _, name := range names {
			if similar(name) {
				if found != "" {
					return ""
				}
				found = name
			}
		}
		return found
	}
	// Edit distance counts each letter whose case differs.
	if found := only(func(name string) bool { return strings.EqualFold(operand, name) }); found != "" {
		return found
	}
	return only(func(name string) bool { return distance(operand, name) <= 2 })
}

// distance is the Levenshtein distance between a and b, in bytes.
func distance(a, b string) int {
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(a); i++ {
		diagonal := row[0]
		row[0] = i
		for j := 1; j <= len(b); j++ {
			substitution := diagonal
			if a[i-1] != b[j-1] {
				substitution++
			}
			diagonal = row[j]
			row[j] = min(row[j]+1, row[j-1]+1, substitution)
		}
	}
	return row[len(b)]
}

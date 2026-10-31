package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
)

func activityCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "activity [--since <time>] [--full] [--json] [<namespace>|<namespace>/<NAME>]...",
		Short: "Show which programs used entries on this Mac",
		Long: `Show what Maskey recorded on this Mac: each get, read, add, update, edit,
delete, grant, and revoke of an entry, its result, and the program that
asked, with the programs that started it and its terminal. The record never
holds values, notes, arguments, or environment variables, and mk activity
never reads the Keychain.

By default, mk groups the records by entry, operation, result, and caller,
with a count and the latest time, newest first; CALLER names each program
by its file name, nearest first, and the window as Maskey. --full lists
each record instead, with full paths, the terminal, and how the owner
approved a grant or revoke. Operands keep only those entries or namespaces.
--since takes a duration such as 24h or 30d, or an RFC 3339 time.

Maskey keeps the current month's file and the 12 before it, and removes
older ones when it writes a new month's first record. --json writes each
matching record as one JSON line, newest first, in the format of those
files in ~/Library/Logs/` + store.Service + `.`,
		Example: `  mk activity
  mk activity --since 24h github
  mk activity --full anthropic/ANTHROPIC_API_KEY`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ArbitraryArgs,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
			return completeNames(cmd.Context(), env, args, true)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			value, _ := cmd.Flags().GetString("since")
			since, err := parseSince(value, time.Now())
			if err != nil {
				return err
			}
			filters, err := parseSelectors(args)
			if err != nil {
				return err
			}
			lines, skipped, err := env.Activity.Read(since, func(r activity.Record) bool {
				return len(filters) == 0 || selects(filters, r.Entry) || r.To != "" && selects(filters, r.To)
			})
			if err != nil {
				return withHint(&store.Error{Kind: kindIO, Message: "cannot read the activity record: " + err.Error()}, "check that you can read ~/Library/Logs/"+store.Service+" and its files")
			}
			var parts []string
			if n := skipped.Unreadable; n > 0 {
				parts = append(parts, count(n, "unreadable line", "unreadable lines"))
			}
			if n := skipped.Newer; n > 0 {
				parts = append(parts, count(n, "record", "records")+" of a later Maskey")
			}
			if len(parts) > 0 {
				warn(cmd, env, "skipped "+strings.Join(parts, " and "))
			}
			full, _ := cmd.Flags().GetBool("full")
			switch {
			case jsonOutput(cmd):
				var text strings.Builder
				for _, line := range lines {
					text.Write(line.Raw)
					text.WriteByte('\n')
				}
				if _, err := io.WriteString(env.Stdout, text.String()); err != nil {
					return outputError()
				}
				return nil
			case len(lines) == 0:
				fmt.Fprintln(env.Stderr, "mk: no activity")
				return nil
			case full:
				return writeTable(env, "TIME\tOP\tENTRY\tRESULT\tCALLER", fullRows(lines))
			default:
				return writeTable(env, "LATEST\tCOUNT\tENTRY\tOP\tRESULT\tCALLER", groupRows(lines))
			}
		},
	}
	cmd.Flags().String("since", "7d", "Show records since this duration ago or `time` (default 7d)")
	cmd.Flags().Bool("full", false, "List each record instead of groups")
	addJSONFlag(cmd, "Write matching records or errors as JSON Lines")
	return cmd
}

// parseSince reads a duration before now, in Go's form or a number of days
// such as 7d, or an RFC 3339 time.
func parseSince(value string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	if d, err := time.ParseDuration(value); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, withHint(usageError("invalid value for --since"), "use a duration such as 24h or 7d, or an RFC 3339 time such as 2026-11-01T09:00:00Z")
}

func selects(filters []store.Ref, entry string) bool {
	namespace, name, _ := strings.Cut(entry, "/")
	for _, filter := range filters {
		if filter.Namespace == namespace && (filter.Name == "" || filter.Name == name) {
			return true
		}
	}
	return false
}

const timeLayout = "2006-01-02 15:04"

// caller names a program, then its ancestors nearest first, by file name
// unless full.
func caller(c activity.Caller, full bool) string {
	programs := slices.DeleteFunc(append([]string{c.Exe}, c.Chain...), func(p string) bool { return p == "" })
	if !full {
		for i, program := range programs {
			programs[i] = filepath.Base(program)
		}
	}
	return strings.Join(programs, " < ")
}

// subject is a record's entry, and the reference a rename gave it.
func subject(line activity.Line) string {
	if line.To != "" {
		return line.Entry + " -> " + line.To
	}
	return line.Entry
}

// groupRows folds lines, newest first, into one row per entry, operation
// with what a read was for, result, and caller, in the order of their
// latest record. Callers group by full path and show by file name.
func groupRows(lines []activity.Line) []string {
	type group struct {
		latest time.Time
		count  int
		row    string
	}
	groups := map[string]*group{}
	var order []*group
	for _, line := range lines {
		op := line.Op
		if line.Via != "" {
			op += " (" + line.Via + ")"
		}
		row := strings.Join([]string{subject(line), op, line.Result}, "\t")
		key := row + "\t" + caller(line.Caller, true)
		if g, ok := groups[key]; ok {
			g.count++
			continue
		}
		g := &group{line.At, 1, row + "\t" + caller(line.Caller, false)}
		groups[key] = g
		order = append(order, g)
	}
	rows := make([]string, len(order))
	for i, g := range order {
		rows[i] = fmt.Sprintf("%s\t%d\t%s", g.latest.Local().Format(timeLayout), g.count, g.row)
	}
	return rows
}

func fullRows(lines []activity.Line) []string {
	rows := make([]string, len(lines))
	for i, line := range lines {
		op, who := line.Op, caller(line.Caller, true)
		if detail := line.Via + line.Auth; detail != "" {
			op += " (" + detail + ")"
		}
		if line.Caller.TTY != "" {
			who += " on " + line.Caller.TTY
		}
		rows[i] = strings.Join([]string{line.At.Local().Format(time.DateTime), op, subject(line), line.Result, who}, "\t")
	}
	return rows
}

func writeTable(env *Env, heading string, rows []string) error {
	table := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, heading)
	for _, row := range rows {
		fmt.Fprintln(table, row)
	}
	if table.Flush() != nil {
		return outputError()
	}
	return nil
}

// count is n of something, singular for one.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

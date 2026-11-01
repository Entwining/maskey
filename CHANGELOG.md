# Changelog

## 0.0.4

- In a sandbox that cannot write `~/Library/Logs/io.github.entwining.maskey`, `mk` sends the operations `mk activity` shows to the Keychain service, which records them from outside the sandbox, so the sandbox needs only the service's socket. Remove that directory from the sandbox's writable paths if you added it: a process that can write there can rewrite or delete the record. Without the service, `mk` still uses the Keychain directly where it can, and its warning says that the operation was not recorded and how to fix that.
- `mk setup` while the Keychain service is running restarts it instead of sometimes failing and leaving it stopped, which made `mk grant`, `mk revoke`, and sandboxed commands report the service unreachable until the next `mk setup`.
- Hide the window's sidebar with the button at its top edge or View → Hide Sidebar (Control-Command-S), and show it again the same way; the entry takes its room, and the chosen namespace stays chosen.
- Drag the line between the sidebar and the list, or between the list and the entry, to resize them, or move it with the arrow keys once Tab reaches it. Dragging the sidebar's line more than halfway from its narrowest width to the window's edge hides the sidebar, and dragging back before letting go shows it again. The window remembers whether the sidebar is hidden and how wide the sidebar and the list are, and it can now be as narrow as 740 points.

## 0.0.3

- `mk run` takes its selectors as arguments before a required `--`: `mk run github work/GITHUB_TOKEN -- gh pr list` passes the namespace `github` with the work token in place of its own. `-n` and `-e` are gone, and using them explains the new form. `mk`'s flags go before the first selector, so everything after `--`, `--help` included, belongs to the command.
- `mk get <namespace>/<NAME>` writes one entry's raw value for a program that runs a command to obtain its key. It works only for entries you grant program access, and never to a terminal; new entries start without it. `mk list --json` shows which entries have program access, and replacing, editing, or renaming an entry keeps it.
- Program access changes only after you authenticate with your login password, Touch ID, or Apple Watch: with the Program access switch of an entry in the window, or with `mk grant` and `mk revoke`, which go through the Keychain service. One approval covers further changes from the same place for 5 minutes, and `mk` says when a change needed no dialog. Interrupting `mk grant` or `mk revoke` while the dialog is open withdraws the change.
- `mk set` and `mk delete` take several entries. `mk set` prompts for each value, naming the entry, and checks every reference and value before writing any; `mk delete` lists the entries and asks once. Both write in order, stop at the first failure, and report what they completed and what they did not attempt. With `--json`, their results are one `{"results":[...]}` document, also for a single entry.
- `mk set --stdin` drops the final line ending of a one-line value, so `gh auth token | mk set --stdin github/GITHUB_TOKEN` stores the token alone; a value of several lines is still stored byte for byte. Without a terminal, `mk set` needs `--stdin` and `mk delete` needs `--yes`.
- The delete confirmation accepts only a complete `y` or `yes` line; an answer cut short by the end of input no longer deletes.
- Namespaces may contain uppercase letters and are case-sensitive, so `GitHub` and `github` are two namespaces, listed side by side. A Mac still on 0.0.2 rejects such a namespace and leaves its entries out of its lists.
- Errors refer to arguments by position and name an entry only when it exists, and suggest an existing entry or namespace that differs only in case or by up to two typing errors. An invalid namespace or name states the rule it breaks, in the window too. New exit statuses: 1 for an internal error, 11 for an entry without program access, and 12 for input or output that failed. `mk help exit-codes` describes them all.
- Command help starts with usage and lists each command's flags; the root groups core and setup commands. On a terminal, headings, command names, and flags are bold, unless `NO_COLOR` is nonempty or `TERM` is `dumb`.
- `mk activity` shows which programs got, read, wrote, or changed the program access of which entries on this Mac, and with what result, grouped over the last week by default. Each record names the program that ran `mk` and the programs that started it, or Maskey for the window, and refused grants and revokes are recorded too. Maskey keeps the current month's file and the 12 before it in `~/Library/Logs/io.github.entwining.maskey`, without values, notes, arguments, or environment variables.
- In the window, an entry's name and namespace show a tooltip only when they are cut off.

## 0.0.2

- An entry selected with `mk run -e` replaces a same-named entry of a namespace selected with `-n`, instead of failing as a conflict: `mk run -n github -e work/GITHUB_TOKEN -- gh pr list` passes the rest of `github` with the work token. The same name selected from two namespaces otherwise is still a conflict, and `mk run --help` explains why.
- `mk run` in the foreground of the terminal that is its stdin and stdout gives the command a terminal of its own, so interactive and full-screen programs work, with resizing and Ctrl-Z and `fg`. A stderr redirected elsewhere stays a separate masked pipe. Masking matches exact bytes, so a program that breaks a value up on screen, by wrapping or styling part of it, shows it unmasked, and a selected value with a control character keeps pipes. Processes the command leaves behind lose its terminal when it ends; most end by the hangup, but one that ignores it keeps running.
- The window keeps search usable while you edit an entry. Choosing another entry, namespace, or search result, closing the window, or quitting with unsaved changes asks whether to save them, and Cancel or Escape asks before discarding them. The toolbar's buttons keep their places.
- The Edit menu edits (Command-L) or deletes (Command-Backspace) the entry chosen in the list, Edit → Copy copies its value, and View → Show Value (Command-Y) shows or hides it. Speech, which did nothing in Maskey, is gone from the Edit menu. Name, Namespace, and Kind stay on one line in view mode, with truncated text available in tooltips. Revealed values scroll horizontally and keep their own line breaks; notes still wrap. The kind Setting is now called Other.

## 0.0.1

The first release of Maskey: a macOS window and the `mk` command for credentials kept in iCloud Keychain. Manage entries yourself, and let scripts and coding agents use them as environment variables, with exact values masked in the command's output.

### Included

- A window to add, search, reveal, copy, edit, and delete entries, grouped by namespace, with multiline values, notes, and a kind suggested from the name.
- `mk run`, which gives a command the entries you select, whole namespaces or single entries, and masks their values in its stdout and stderr, passing its exit status or signal through.
- `mk list`, `mk set`, `mk rename`, and `mk delete`, none of which prints a value, and `mk doctor` to check the setup.
- A per-user Keychain service, so sandboxed commands reach their credentials over one Unix socket while the command and its masking stay in the sandbox.
- An agent skill, linked into `~/.claude/skills` and `~/.agents/skills` by `mk skill install`, that always matches the installed app.
- Updates from within the app: Maskey checks once a day and offers each new version, and Maskey → Check for Updates… checks at once.

### Know before you use it

- The command receives the credentials. Masking catches exact values in stdout and stderr, not encoded values, files, or network traffic.
- Any program hosted by Apple's `/usr/bin/python3` can reach the same Keychain items, and entries stay readable while the screen is locked after the first unlock since startup.
- Deleting an entry deletes it on every Mac on your Apple Account, and Maskey cannot tell when a change has reached another Mac.

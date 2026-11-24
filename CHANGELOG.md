# Changelog

## 0.0.12

- After upgrading, each Mac needs one successful refresh of each Bitwarden store, such as `mk store sync <name>`, before it can read that store offline. Maskey now selects its entries from the project's key index before fetching values and notes, saves only those entries, and does not read the copy an earlier version saved.
- If a store name is reconnected to another project, entry operations bound to the original connection fail with `conflict`. The window clears entries from the previous project. Its Delete and Remove… confirmations apply only to the connection they showed.
- A grant or revoke the owner approves for a Bitwarden entry no longer reaches a secret created under the same reference while they were asked. Once this Mac has seen the replacement, the command reports `conflict`.
- `mk store remove` asks before disconnecting a project. Without terminal stdin it fails with `usage` (exit status 2) unless you pass `--yes` or `-y`, which removes by name. The interactive confirmation and the `mk store token` prompt report `conflict` if the name has since been connected to another project. `mk store token --stdin` replaces the token by name.
- With `--json`, a successful `mk store add`, `remove`, `token`, `default`, or `sync` returns the store's name in `store` and `added`, `removed`, `token_replaced`, `default_changed`, or `synced` in `action`. Scripts written for 0.0.11 need three changes: `mk store add` returned the connection object in `store` and now returns it in `connection`, `mk store token` returned `token replaced`, and `mk store default` returned `now the default`. `mk store list --json` is unchanged.
- `mk store add` and `mk store token` check the name and local settings before reading the token. Without terminal stdin they fail with `usage` unless you pass `--stdin`. If Bitwarden refuses a token during setup, the hint identifies the region and token to check. `mk doctor` identifies missing project access.
- Sandboxed commands can ask the Maskey service to read `stores.json`, so they can use the default or a named store without read permission for the file.
- A slow Bitwarden connection no longer blocks commands, the window, or Settings from working with other stores. A read that a recent saved copy answers no longer waits for another request to the same store, and removing a store or replacing its token no longer waits for an entry read, write, or sync the store is in.
- `mk store remove` and Remove… in Settings identify the failed step if removal stops after deleting the token. The store stays listed for another removal attempt. `mk uninstall` identifies a partly removed store and keeps the configuration directory and activity record if the legacy Keychain service may still be running.
- Entry commands and `mk activity` reject an empty `--store` with `usage`. Usage hints include the selected store once.
- `mk activity --store <name>` filters history by the recorded store name. Tab completion includes distinct names from that history, even after removal. When `mk` can write the record, it also records a read or change cancelled before the service received it.
- Choose Replace Access Token… in Settings to rotate a store's token while keeping its name and program access. The dialog closes if the name comes to mean another connection. After a failed Add or Replace, Undo cannot restore the cleared token.
- Quitting Maskey, also to relaunch after an update, waits for a store change in Settings to finish. If it fails, Maskey stays open to show the error.
- Settings shows progress while reading this Mac's stores and offers Try Again if reading fails. New Entry stays disabled until the stores are known.
- The Delete confirmation names the entry and store. It explains that an iCloud deletion reaches every Mac syncing that Keychain, and that a Bitwarden deletion moves the entry to trash for everyone using the project.
- `mk delete` and its help no longer say that a Bitwarden deletion cannot be undone: it moves the entries to Bitwarden's trash, and Maskey cannot restore them.
- The form's Terminal commands for CRLF input, existing carriage returns, and protected SSH keys select the entry's store and take input from a file. New Entry names the typed reference only when it is valid and the store's entries have been read and show none of that name, and leaves a placeholder otherwise, so the command does not replace an existing entry. When `mk set` would store another kind than the form shows, the command names a `<NAME>_SSH_KEY` entry for an SSH Key, and for another kind says to set Kind in the window afterwards. Changing stores clears the suggestion.
- New Entry shows a note under Name when the reference differs only in case from another in the same store.
- Switching between one store and All Stores keeps the selected namespace and entry while their store is visible. The Stores Shown button has a larger target, responds to hover, and is reachable separately with Tab or VoiceOver.
- The sidebar's default width is now 220 points. Double-click its divider to restore that width.

## 0.0.11

- Entries can live in a Bitwarden Secrets Manager project as well as in iCloud Keychain, so that Macs on different Apple Accounts can share them. Connect a project in Maskey > Settings… or with `mk store add bitwarden`; each Mac keeps its own access token in its Keychain. Commands work on this Mac's default store or the one `--store <name>` names, the window lists each store's namespaces under the store's name and can show one store alone, and `mk store` lists, syncs, removes, and sets the default. Reading keeps working from an encrypted copy while Bitwarden cannot be reached; changes need it.
- New error kinds for a store that is not on this Mac: `network` (exit status 13), `unauthenticated` (14), `permission_denied` (15), `rate_limited` (16), and `remote_error` (17). `mk help exit-codes` describes them.
- `mk activity` also records an operation that could not be performed because neither the Keychain nor the Maskey service was reachable, one on a store the service could not open, and a new entry the window could not create because it exists.
- `mk activity` shows each entry after its store, as `icloud:github/GITHUB_TOKEN`, `mk doctor` reads each Bitwarden store from Bitwarden, `mk store token` replaces a store's expired access token, and `mk uninstall` removes this Mac's Bitwarden stores, with their access tokens, before the Maskey service.
- The Keychain service is now the Maskey service, since it also reaches Bitwarden stores, and its socket is `~/Library/Application Support/io.github.entwining.maskey/service.sock`: a sandbox that allowed `keychain.sock` must allow the new path, and one that restricts reading must allow `stores.json` beside it. After the update, run `mk setup` in a normal terminal, or log in again, to replace the earlier service; until then `mk grant`, `mk revoke`, and sandboxed commands fail with `unavailable`.
- `mk` reaches the Maskey service from a sandbox that keeps it from inspecting other processes, such as Claude Code's, where it failed with "the Maskey service identity could not be verified".
- Double-click the line between the sidebar and the list, or between the list and the entry, to return the pane before it to its width in a new window.
- In dark mode, tooltips are dark with light text, as macOS's are, instead of near white.
- In dark mode, the update button under the sidebar lightens while the pointer is over it, instead of looking the same as at rest.
- In an entry's details, the Show Value and Copy Value icons and the Program access switch end at the card's inner edge, where the edit form's fields end, instead of short of it and out of line with each other.

## 0.0.10

- When Maskey finds a newer version, Check for Updates… in the Maskey menu becomes Update to Maskey <version>…, and a button under the sidebar names the version; both open the update window, which offers to relaunch into an update already installed in the background. An update installed without asking still waits for Maskey to quit, and if Maskey stays open a week after finding it, the update window shows once, the next time you switch to Maskey.

## 0.0.9

- An SSH Key entry gives the command `mk run` starts an SSH agent instead of the key: `ssh`, `git`, and `ssh-keygen -Y sign` sign with it through `SSH_AUTH_SOCK`, the entry's name holds its public key, and the agent ends with the command, so the private key never enters the command's environment or an agent that outlives it. A new entry whose name ends in `SSH_KEY` gets this kind; choose it in the window for an existing Private Key entry, which still reaches the command as a variable for programs that read the key themselves. An SSH Key entry holds only a private key without its passphrase, which `mk run` does not ask for: `mk set` removes the passphrase at a terminal prompt, and both `mk set` and the window refuse any other value, such as a public key. Update Maskey on every Mac before choosing SSH Key: an earlier version passes such an entry to commands as a variable, and editing its notes in an earlier window also sets its kind, on every Mac, back to the one its name suggests.
- `mk set` no longer offers to remove a private key's passphrase for an entry of another kind than SSH Key: it stores the key as it is, for a program that reads the passphrase from another entry, and warns, with or without a terminal. To use such a key with `ssh` or `git`, import it into an SSH Key entry, such as a new one whose name ends in `SSH_KEY`, where `mk set` removes the passphrase; an encrypted PKCS#8 key, which `mk set` cannot decrypt, goes through `openssl pkey` first, as the warning shows.
- After an update, `mk` waits up to 15 seconds instead of 5 for the Keychain service to restart on the new version, so the first command no longer fails while macOS holds the restart back for about 10 seconds. From the update after this one, commands that start during that time wait too, instead of failing at once; a Keychain service from 0.0.8 or earlier removes its socket when it stops, which tells them that no service is installed. macOS still writes one crash report for Maskey per update, which names a Launch Constraint Violation.

## 0.0.8

- `mk set` at its hidden prompt lists the entries that already exist and asks once before replacing their values, which the Keychain does not keep; `--yes` skips the question. `mk set --stdin` still replaces without asking, so scripts that rotate a value keep working.
- `mk set` warns before creating an entry whose name differs from an existing one only in case, such as `github/github_token` beside `github/GITHUB_TOKEN`, which `mk run` would set as two variables.
- Hints on stderr and `recovery` in JSON errors and warnings use one form: a lowercase fragment without a final period.
- `mk run` no longer repeats the command in `command_not_found` and `command_not_executable` errors, so a value put after `--` by mistake stays out of stderr and JSON errors. The message says why the command cannot run, and the hint how to fix it, including that `mk run` starts the command without a shell. Error kinds and exit statuses are unchanged; scripts that matched the old message text need the kind instead.
- `mk setup`, `mk uninstall`, and the window's Install Command Line Tool… that find another of them changing the Keychain service say so; `mk setup` and the window said that the service was already running, and `mk uninstall` did not name another `mk uninstall`. One that starts just as `mk uninstall` finishes no longer goes ahead at the same time as another.
- `mk uninstall` keeps the activity record when it cannot stop the Keychain service, which writes the record, and reports it `kept`; run it again once the service is stopped.
- When the connection to the Keychain service ends after `mk grant` or `mk revoke` sent its change, the `uncertain` error says that the change may be incomplete or still running, instead of telling you to check one entry.
- When `mk grant` or `mk revoke` changes entries but cannot write its result, its `io` error lists the completed changes in `completed`, with an empty `not_attempted`, and how the owner approved them in `auth`. An `io` error from `mk rename` says the entry was renamed. The exit status stays 12.
- `mk set`, `mk delete`, and `mk uninstall` ask their questions and read hidden values on the terminal instead of stdin and stderr, so redirecting stderr keeps the prompt visible and `--json` keeps stderr to JSON lines. Whether they ask still depends on stdin: without a terminal there, they need `--stdin` or `--yes` as before.
- `mk skill install` from an `mk` outside Maskey.app says to use the `mk` that Maskey > Install Command Line Tool links into the app, and `mk doctor` reports that case instead of suggesting `mk skill install`.
- When `mk set` cannot remove a key's passphrase, its hint no longer claims that `ssh-keygen -p` keeps the passphrase: it says to enter the same one at the new-passphrase prompts.
- `mk doctor` reports `1 entry in 1 namespace` instead of `1 entries in 1 namespaces`, in its text and in the JSON `detail`.
- `mk help exit-codes` shows the `{"error":{...}}` wrapper of JSON errors, the shape of `entry`, every `action` a result can have, and where prompts go; its `locked` and `unavailable` rows describe the state and name mk's Keychain service.
- Command help uses consistent usage lines, operand names, and wording. `mk activity --help` says which commands its operations come from, and each `mk completion <shell> --help` says how to load completion in every session.
- In the window's entry list, holding Space, Command-C, or Command-Backspace acts once, as a single press does, instead of showing and hiding the value, reading it from the Keychain each time it shows, or copying it over and over.
- Copy Value, Edit > Copy, and Command-C in the entry list keep the copied value on this Mac, off Universal Clipboard, mark it as confidential for clipboard managers, and clear it after 90 seconds or when Maskey quits, unless something else was copied since. Text selected in a revealed value and copied is ordinary text.
- A divider dragged quickly and let go between two frames ends where the pointer let go instead of short of it, so a quick drag of the sidebar's line toward the window's edge hides the sidebar.
- Closing the window or quitting while it saves, deletes, or changes program access of an entry, or installs the command line tool, waits for that to finish and then goes ahead, instead of ending Maskey partway, before `mk activity` records the change. If it fails, the window stays open and shows the failure.
- After creating an entry, searching before the list shows it no longer brings that entry back when the search is cleared, in place of the entry chosen from the results.
- Show Value and Copy Value read the value again when the list shows that it was replaced while being read, so they show or copy the new value instead of the replaced one.

## 0.0.7

- For a private key protected by a passphrase, `mk set --stdin` asks on the terminal whether to store the key without it, so that `ssh-add -` and other programs load it without asking: enter the passphrase once, or press Return to keep it for a program that reads the passphrase from another entry, such as Snowflake's `PRIVATE_KEY_PASSPHRASE`. The key keeps its format, OpenSSH or PEM, but not its comment. Without a terminal, or for a PKCS#8 key, which `mk` cannot decrypt, the key is stored as it is and a warning says how to remove the passphrase.

## 0.0.6

- In the window, Namespace comes before Name, in an entry's details and in its form, as in `<namespace>/<NAME>`. A new entry starts in Namespace when All Entries is chosen, and in Name when the namespace chosen in the sidebar fills Namespace, also when it replaces another form. Leaving a new entry asks first once you have typed or changed its namespace.

## 0.0.5

- In an entry's details, the Show Value and Copy Value buttons line up with the value's first line instead of sitting lower, and the Program access switch with its state, so every field is as tall as one line of text.

## 0.0.4

- In a sandbox that cannot write `~/Library/Logs/io.github.entwining.maskey`, `mk` sends the operations `mk activity` shows to the Keychain service, which records them from outside the sandbox, so the sandbox needs only the service's socket. Remove that directory from the sandbox's writable paths if you added it: a process that can write there can rewrite or delete the record. Without the service, `mk` still uses the Keychain directly where it can, and its warning says that the operation was not recorded and how to fix that.
- `mk uninstall` removes what Maskey installed on this Mac outside its app: it stops the Keychain service and removes its configuration, the activity record, the window's saved state, and the `mk` and skill links, listing them before it asks. Entries and Maskey.app stay.
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

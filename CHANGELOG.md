# Changelog

## 0.0.2

- An entry selected with `mk run -e` replaces a same-named entry of a namespace selected with `-n`, instead of failing as a conflict: `mk run -n github -e work/GITHUB_TOKEN -- gh pr list` passes the rest of `github` with the work token. The same name selected from two namespaces otherwise is still a conflict, and `mk run --help` explains why.
- `mk run` in the foreground of the terminal that is its stdin and stdout gives the command a terminal of its own, so interactive and full-screen programs work, with resizing and Ctrl-Z and `fg`. A stderr redirected elsewhere stays a separate masked pipe. Masking matches exact bytes, so a program that breaks a value up on screen, by wrapping or styling part of it, shows it unmasked, and a selected value with a control character keeps pipes. Processes the command leaves behind lose its terminal when it ends; most end by the hangup, but one that ignores it keeps running.

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

# Changelog

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

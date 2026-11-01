<p align="center">
  <img src="resources/icon.png" alt="" width="128">
</p>
<h1 align="center">Maskey</h1>

Maskey keeps API keys, tokens, and other credentials in iCloud Keychain and gives them to commands as environment variables. You manage entries in a macOS window; coding agents and scripts use them through the `mk` command, without putting the values in prompts or project files.

```sh
mk set github/GITHUB_TOKEN          # prompts for the value without echoing it
mk run github -- gh auth status     # gh gets GITHUB_TOKEN; its output shows *** in place of the value
```

An entry is `<namespace>/<NAME>`. The name becomes the environment variable, and the namespace groups entries that a command can receive together; it is not a separate vault.

## What Maskey protects, and what it does not

`mk run` replaces each exact value in the command's stdout and stderr with a marker before passing the output on. The window shows a value when you reveal, copy, or edit it. `mk get` prints one only for an entry you granted program access, for a program that runs a command to obtain its key; once granted, any program that can run `mk` can obtain that value. Changes to program access require your login password, Touch ID, or Apple Watch; one approval covers further changes from the same place for 5 minutes.

The command you run still receives the credentials and can do anything with them. Masking does not catch a value the command encodes or transforms, writes to a file, or sends over the network. A terminal program that inserts line breaks or styling within a value can also show it unmasked. Use Maskey with commands you trust, and wrap the tool that needs the credential rather than a whole agent, whose own logs `mk` cannot mask.

Maskey reaches the Keychain through Apple's `/usr/bin/python3`, so any program hosted by that interpreter can reach the same items, without asking you and without a record. The activity record and the 5-minute approval for program access are Maskey's own policy, not protections macOS enforces, and a program running as you can also edit the record unless a sandbox keeps it from writing the record's directory. Entries stay readable after the Mac's first unlock since startup, so unattended jobs keep working while the screen is locked.

## Install

You need an Apple silicon Mac and Apple's Command Line Tools (`xcode-select --install`) or Xcode, which provide `/usr/bin/python3`. To have entries on several Macs, turn on iCloud Keychain on each with the same Apple Account and install Maskey on each; iCloud Keychain does the syncing, and Maskey has no sync settings of its own.

### Homebrew

```sh
brew install --cask entwining/tap/maskey
```

This puts Maskey in `/Applications` and `mk` on your `PATH`. Maskey is signed ad hoc, without a Developer ID, and is not notarized, so Gatekeeper blocks the app Homebrew downloaded. Remove its quarantine attribute:

```sh
xattr -dr com.apple.quarantine /Applications/Maskey.app
```

Maskey then updates itself. `brew upgrade` also replaces it with each new version, which is quarantined again because an ad hoc signature cannot carry your approval to the next version; set `HOMEBREW_NO_UPGRADE_AUTO_UPDATES_CASKS=1` to leave updates to Maskey, or run the `xattr` command again after an upgrade.

### Install script

```sh
curl -fsSL https://github.com/Entwining/maskey/releases/latest/download/install.sh | sh
```

The script downloads the latest version, checks its signature against the key Maskey's updates are signed with, installs it in `/Applications` (or `~/Applications` when it cannot write there), and opens it. `curl` sets no quarantine attribute, so Gatekeeper does not block it.

Each release's archive also has a [build provenance attestation](https://cli.github.com/manual/gh_attestation_verify): `gh attestation verify maskey-<version>-darwin-arm64.tar.gz --repo Entwining/maskey`.

### Build from source

With Go at the version in [go.mod](go.mod) or later, run in a checkout:

```sh
go tool mygo build -skip-dmg
ditto build/darwin-arm64/Maskey.app /Applications/Maskey.app
```

Like an installed copy, a source build offers to replace itself with each published release.

### Set up the command line

Open Maskey and choose Maskey → Install Command Line Tool…. This links `~/.local/bin/mk` to the app and installs a per-user Keychain service that starts at login without opening a window. Add `~/.local/bin` to your `PATH` if your shell cannot find `mk`. `mk setup` in a terminal installs or reinstalls only the service, which is all a Homebrew install still needs.

For coding agents, `mk skill install` links Maskey's skill into `~/.claude/skills` and `~/.agents/skills`. The links point into the app, so the skill always matches the installed `mk`; `mk skill show` prints it for an agent that reads skills from elsewhere.

### Upgrade

Maskey checks for a new version shortly after it opens and then once a day, and Maskey → Check for Updates… checks at once. It installs a version only after checking its signature, and the new version runs once you restart Maskey. Nothing needs reinstalling: entries stay in the Keychain, `mk` and the skill links point into the app, and the Keychain service restarts from the new version the first time the new `mk` reaches it.

### Uninstall

Entries stay in iCloud Keychain, on every Mac, when Maskey is gone; delete them in the window or with `mk delete` first if you no longer want them. Then quit Maskey, stop the Keychain service, and remove what Maskey installed and its activity record, whose files are append-only:

```sh
launchctl bootout gui/$(id -u)/io.github.entwining.maskey.keychain
rm -f ~/Library/LaunchAgents/io.github.entwining.maskey.keychain.plist* ~/.local/bin/mk ~/.claude/skills/maskey ~/.agents/skills/maskey
chflags -R nouappnd ~/Library/Logs/io.github.entwining.maskey
rm -rf ~/Library/Application\ Support/io.github.entwining.maskey ~/Library/Logs/io.github.entwining.maskey /Applications/Maskey.app ~/Applications/Maskey.app
```

With Homebrew, run `brew uninstall --cask maskey` in place of removing the app.

## Use

### In the window

Click + or choose File → New Entry, and enter a name such as `GITHUB_TOKEN`, a namespace, and the value. Kind describes the value and sets the entry's icon; Maskey suggests one from the name until you choose one. Notes are plain text shown with the entry, so keep secrets in the value. The toolbar's search field (⌘F) matches entry and namespace names across every namespace and stays usable while you edit.

Before you switch entries or namespaces, close the window, or quit, Maskey asks whether to save, discard, or continue editing any unsaved changes. Saving is offered only when the form can be saved. Cancel and Escape in the editor ask before discarding unsaved changes, without offering Save; an unchanged form closes at once. Escape in the search field clears the query.

The value editor accepts multiple lines but refuses to paste CRLF line endings, which it would change to LF. An entry whose value contains carriage returns keeps that value when you edit it; replace it with `mk set --stdin`, which stores a value of several lines byte for byte.

### From a terminal

`mk run <namespace> -- <command>` sets every entry of the namespace as a variable of the same name in the command's environment, replacing an inherited one, and `mk run <namespace>/<NAME> -- <command>` sets one entry. Put `mk` flags before the first selector and list selectors before the required `--`; everything after it belongs to the command. An entry you name replaces a namespace's entry of that name. `mk run` starts the command without a shell, so use `sh -c '…'` for pipes or variable expansion.

Interactive and full-screen programs work when `mk run` runs in the foreground of the terminal that is its input and output; Ctrl-Z suspends the command and `fg` resumes it. Captured output or selected values containing control characters use pipes instead.

`mk list` prints entry references, and `mk list --json` adds each entry's kind, modification time, and whether it has program access; neither shows values or notes. `mk set`, `mk rename`, and `mk delete` manage entries. `mk set` prompts for each value without echoing it; `mk set --stdin` reads one value from a pipe or file, and a terminal echoes what you type to it. Deleting an entry removes it from every Mac on your Apple Account and cannot be undone.

`mk --help` and `mk <command> --help` describe the commands, and `mk help exit-codes` the error kinds and exit statuses that scripts can rely on. Commands that support `--json` list it in their help. With that flag, errors go to stderr as JSON and results to stdout. `mk activity --json` emits one JSON record per line, and `mk doctor --json` keeps failed checks in its stdout report. `mk get` still writes the raw value, and `mk run` passes the command's masked output through.

### Program access and activity

An entry's Program access switch allows `mk get` to give its value to programs; new entries start without it. `mk grant` and `mk revoke` change the same setting through the Keychain service that `mk setup` installs, with the same authentication as the window; interrupting them while the dialog is open withdraws the change. The service and the window each keep their own 5-minute approval. Replacing, editing, or renaming an entry preserves its program access.

`mk activity` shows which programs got, read, wrote, or changed the program access of which entries on this Mac, grouped over the last week unless you ask for more. Each record names the program that ran `mk` and the programs that started it, or Maskey for the window, and grants and revokes you refused are recorded too. Maskey keeps the current month's file and the 12 before it in `~/Library/Logs/io.github.entwining.maskey`, removing older ones when it writes a new month's first record, and never records values, notes, arguments, or environment variables.

## Sandboxed agents

An agent sandbox needs only Maskey's Keychain service to run `mk`. Allow this Unix socket in the sandbox's network permissions:

```text
~/Library/Application Support/io.github.entwining.maskey/keychain.sock
```

`mk` uses the Keychain directly where the sandbox allows it, except for the operations `mk activity` shows when the sandbox cannot write the activity record: those go to the service, which performs and records them outside the sandbox. Do not let a sandbox write `~/Library/Logs/io.github.entwining.maskey`, since a process that can write there can rewrite or delete the record. If the service cannot be reached, `mk` still uses the Keychain directly where it can, and warns that the operation was not recorded. Both ends check that they are the same signed executable before a request or value is sent. Only Keychain operations go through the service; the command and the masking of its output stay in the sandbox.

## Troubleshooting

`mk doctor` checks Apple's interpreter, Keychain access, the service, and the skill links. It cannot check iCloud Keychain sync: Maskey sees only this Mac's Keychain, and iCloud does not report when a change reaches another Mac.

- `unavailable`: neither the Keychain nor the service is reachable. Allow the socket above, or run `mk setup` in a normal terminal if `mk doctor` shows the service is not running.
- `uncertain`: a write may have happened. Check the entry in Maskey before repeating it; a list of names cannot show whether a new value was saved.
- `locked`: unlock the Mac.

## Development

Maskey is written in Go with [MyGo](https://github.com/egoist/mygo). [AGENTS.md](AGENTS.md) lists the checks and the rules for changing it.

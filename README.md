<p align="center">
  <img src="resources/icon.png" alt="" width="128">
</p>
<h1 align="center">Maskey</h1>

Maskey keeps API keys, tokens, passwords, and SSH keys in iCloud Keychain. Manage them in a macOS window, and let scripts and coding agents use them through `mk`: ordinary entries become environment variables with their values masked in output; SSH Key entries provide an SSH agent that ends with the command.

Entries sync between Macs through iCloud Keychain on your Apple Account. Maskey needs no additional account or hosted service, and `mk` has no bootstrap token of its own to store. A local activity record shows which programs used or changed entries through Maskey.

```sh
mk set github/GITHUB_TOKEN          # prompts for the value without echoing it
mk run github -- gh auth status     # gh gets GITHUB_TOKEN; its output shows *** in place of the value
```

An entry is `<namespace>/<NAME>`. The name becomes the environment variable, and the namespace groups entries that a command can receive together; it is not a separate vault. For an SSH Key entry, the variable holds the public key, not the private key.

## What Maskey protects, and what it does not

With `mk run`, you can give a tool credentials without putting them in prompts or project files. It replaces their exact values in the command's stdout and stderr with a marker. The command still receives ordinary credentials and can do anything with them. Masking does not catch a value the command encodes or transforms, writes to a file, or sends over the network. A terminal program that inserts line breaks or styling within a value can also show it unmasked. Use commands you trust, and wrap the tool that needs the credential rather than a whole coding agent, whose own logs `mk` cannot mask.

For an SSH Key entry, `mk run` reads the private key into its own process and gives the command access to signatures instead of the key. During the run, the command can ask for any signature with that key; Maskey does not approve each signature or restrict it to a host or purpose. The private key is still stored and synchronized in iCloud Keychain, not bound to one device or held in hardware. The window can reveal, copy, or edit it, and granting it Program access lets `mk get` return it like any other value.

Maskey reaches the Keychain through Apple's `/usr/bin/python3`, so any program hosted by that interpreter can reach the same items, without asking you and without a record. The activity record and the 5-minute approval for program access are Maskey's own policy, not protections macOS enforces, and a program running as you can also edit the record unless a sandbox keeps it from writing the record's directory. Entries stay readable after the Mac's first unlock since startup, so unattended jobs keep working while the screen is locked.

## Install

You need an Apple silicon Mac and Apple's Command Line Tools (`xcode-select --install`) or Xcode, which provide `/usr/bin/python3`. To use entries on several Macs, turn on iCloud Keychain on each with the same Apple Account and install Maskey on each. Maskey has no sync settings of its own and cannot tell when a change reaches another Mac.

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

Open Maskey and choose Maskey > Install Command Line Tool. This links `~/.local/bin/mk` to the app and installs a per-user Keychain service that starts at login without opening a window. Add `~/.local/bin` to your `PATH` if your shell cannot find `mk`. For a Homebrew install, run `mk setup` in a normal terminal to install the service; Homebrew already supplies the `mk` link.

For coding agents, `mk skill install` links the [shipped skill](resources/skills/maskey/SKILL.md), which teaches agents to use credentials without putting their values in the conversation, into `~/.claude/skills` and `~/.agents/skills`. The links point into the app, so the skill matches the installed `mk`; `mk skill show` prints it for an agent that reads skills from elsewhere. Agents whose sandbox blocks Keychain access also need the socket permission described under [Sandboxed agents](#sandboxed-agents).

## Use

### Manage entries

In the window, click + or choose File > New Entry and enter a namespace, a name such as `GITHUB_TOKEN`, and the value. The namespace chosen in the sidebar is filled in. Kind is suggested from the name until you choose one; SSH Key changes how `mk run` supplies the key, while the other kinds describe the value and set its icon. Notes are plain text shown with the entry, so keep secrets in the value. Search (⌘F) matches entry and namespace names across every namespace, including while you edit; Escape in the search field clears the query.

The window asks what to do with unsaved changes before switching entries or namespaces, closing, or quitting. You can save when the form is valid, discard, or continue editing. Cancel and Escape in the editor ask before discarding changes without offering Save; an unchanged form closes at once.

The value editor accepts multiple lines but refuses to paste CRLF line endings, which it would change to LF. An existing value containing carriage returns stays unchanged when you edit the entry; replace it with `mk set --stdin` to preserve its line endings. The window does not remove a private key's passphrase, so it refuses a protected key for an SSH Key entry; import that key from your terminal with `mk set --stdin`.

You can reveal or edit a value in the window without granting program access. Copy Value keeps it on this Mac, marks it as confidential for clipboard managers, and clears it after 90 seconds or when Maskey quits, unless something else was copied since. Text selected in a revealed value and copied is ordinary text.

From a terminal, `mk set` prompts without echoing the value and asks before replacing an existing one. `mk set --stdin` reads from a pipe or file and replaces without asking; a terminal echoes what you type to it. A one-line value loses its final line ending, while a value of several lines is stored byte for byte unless `mk` removes a private key's passphrase. For a protected private key in an entry of another kind than SSH Key, `mk set` offers on the terminal to remove the passphrase; press Return to keep it for a program that reads it separately.

`mk list` shows entry references, and `mk list --json` adds kind, modification time, and program access, without values or notes. Names are case-sensitive; `mk` warns before creating a name that differs from an existing one only in case. `mk rename` moves an entry with its value and details. Deleting an entry in the window or with `mk delete` removes it from every Mac syncing this Keychain and cannot be undone.

### Run a command

Give a command one entry or a whole namespace:

```sh
mk run github/GITHUB_TOKEN -- gh pr list
mk run github -- gh auth status
```

Each selected entry sets a variable of the same name, replacing an inherited one. You can combine namespaces and individual entries; an entry you name overrides a namespace's entry of that name. `mk run` starts the command without a shell, so use `sh -c '…'` for pipes or variable expansion in the child. Keep secret values out of command arguments.

Interactive and full-screen programs work when `mk run` runs in the foreground of the terminal that is its input and output; Ctrl-Z suspends the command and `fg` resumes it. Captured output or selected values containing control characters use pipes instead.

`mk --help` and `mk <command> --help` describe command syntax, flags, and examples. For scripts, `mk help exit-codes` describes error kinds, exit statuses, JSON output, and how to handle partial or uncertain writes.

### SSH keys

Choose SSH Key (`ssh`) in the window for a key used by OpenSSH. A new entry whose name ends in `SSH_KEY` gets this kind automatically; choose it yourself for other names or an existing Private Key entry. Private Key (`pem`) still supplies the private key as a variable for programs that read a key themselves.

Import a key from your terminal, then run the command that needs it:

```sh
mk set --stdin deploy/DEPLOY_SSH_KEY < /path/to/private-key
mk run deploy/DEPLOY_SSH_KEY -- ssh user@host
```

An SSH Key entry holds only a private key `mk run` can lend, without its passphrase. When importing a protected key, `mk set` asks for the passphrase on the terminal and removes it before storing the key. If it cannot ask or cannot decrypt the key, or the value is not a private key, such as a `.pub` file, it writes no entry and explains how to proceed. Removing the passphrase keeps the key's format but drops its comment; the source file is unchanged.

`mk run` sets `DEPLOY_SSH_KEY` to the public key in `authorized_keys` form and `SSH_AUTH_SOCK` to its own SSH agent. The private key does not enter the command's environment or a temporary file. The agent works with `ssh`, Git over SSH, and `ssh-keygen -Y sign`; it ends with the command, closing open agent connections too.

For [SSH commit signing](https://git-scm.com/docs/git-config#Documentation/git-config.txt-usersigningKey) with a key stored as `signing/SIGNING_SSH_KEY`, give Git the public key from the variable:

```sh
mk run signing/SIGNING_SSH_KEY -- sh -c 'git -c gpg.format=ssh -c user.signingKey="key::$SIGNING_SSH_KEY" commit -S -m "<message>"'
```

Select only the keys the command needs, since a server counts each offered key as an authentication attempt. An [`IdentityAgent` setting](https://man.openbsd.org/ssh_config#IdentityAgent) in `ssh_config` takes precedence over `SSH_AUTH_SOCK`; pass `ssh -o IdentityAgent=SSH_AUTH_SOCK`, or `git -c core.sshCommand='ssh -o IdentityAgent=SSH_AUTH_SOCK'`, to use Maskey's agent. Agent forwarding with `ssh -A` lends the agent to the remote host for the run, allowing it to request signatures too.

### Program access

`mk run` needs no Program access grant. Turn on an entry's Program access switch only for a program that must obtain its raw value from a helper command, such as `mk get <namespace>/<NAME>`. Configure that program to capture the output itself; `mk get` refuses terminal stdout. New entries start without program access, and a grant applies to any program that can run `mk` with access to the Keychain, not just the intended consumer.

The switch and `mk grant` or `mk revoke` require your login password, Touch ID, or Apple Watch. The commands use the Keychain service installed by `mk setup`; interrupting them while the approval dialog is open withdraws the change. One approval covers further changes for 5 minutes, with the service and the window each keeping their own approval. Replacing, editing, or renaming an entry preserves its program access.

### Activity

`mk activity` shows which programs got, read, wrote, deleted, or changed program access to which entries on this Mac, grouped over the last week unless you ask for more. Each record names the program that ran `mk` and the programs that started it, or Maskey for the window; refused grants and revokes are recorded too. For an SSH key, the read at the start of `mk run` is recorded, but individual signatures are not.

The record never contains values, notes, command arguments, or environment variables. Maskey keeps the current month's file and the 12 before it in `~/Library/Logs/io.github.entwining.maskey`, removing older ones when it writes a new month's first record.

## Sandboxed agents

A sandbox that blocks direct Keychain access can use Maskey's per-user Keychain service. Allow this Unix socket in the sandbox's network permissions:

```text
~/Library/Application Support/io.github.entwining.maskey/keychain.sock
```

`mk` uses the Keychain directly where allowed, except for recorded operations when the sandbox cannot write the activity record: those go to the service, which performs and records them outside the sandbox. Do not let a sandbox write `~/Library/Logs/io.github.entwining.maskey`, since a process that can write there can rewrite or delete the record. If the service cannot be reached, `mk` still uses the Keychain directly where it can, and warns when an operation was not recorded.

Both ends check that they are the same signed executable before a request or value is sent. Only Keychain operations and program access approval go through the service; the command and the masking of its output stay in the sandbox. `mk` authenticates with its executable identity, not a stored token.

For SSH Key entries, `mk run` also creates an agent socket in a private directory under `TMPDIR`. The sandbox must allow `mk` to create Unix sockets there and the command to connect to them. This socket lasts only for the run and is separate from the Keychain service's socket above.

## Troubleshooting

`mk doctor` checks Apple's interpreter, Keychain access, the service, and the skill links. It reads entry attributes, not values. It cannot check iCloud Keychain sync: Maskey sees only this Mac's Keychain, and iCloud does not report when a change reaches another Mac.

- `unavailable`: neither the Keychain nor the service is reachable. Allow the socket under [Sandboxed agents](#sandboxed-agents), or run `mk setup` in a normal terminal if `mk doctor` shows the service is not running.
- `uncertain`: a write may have happened. Check the entry in Maskey before repeating it; a list of names cannot show whether a new value was saved.
- `locked`: unlock the Mac.

## Upgrade

Maskey checks for a new version shortly after it opens and then once a day, and Maskey > Check for Updates checks at once. It installs a version only after checking its signature, and the new version runs once you restart Maskey. Entries stay in the Keychain, `mk` and the skill links point into the app, and the Keychain service restarts from the new version the first time the new `mk` reaches it.

Because each version's ad hoc signature differs, macOS can refuse that restart once, write a crash report for Maskey that names a Launch Constraint Violation, and start the new version about 10 seconds later; `mk` waits for it.

## Uninstall

Entries stay in iCloud Keychain, on every Mac, when Maskey is gone; delete them in the window or with `mk delete` first if you no longer want them. Quit Maskey, then run `mk uninstall`: it lists what Maskey installed on this Mac, asks once, stops the Keychain service, and removes its configuration, the activity record, the window's saved state, and the `mk` and skill links. Then move Maskey.app to the Trash, or run `brew uninstall --cask maskey` if Homebrew installed it.

## Development

Maskey is written in Go with [MyGo](https://github.com/egoist/mygo). [AGENTS.md](AGENTS.md) lists the checks and the rules for changing it.

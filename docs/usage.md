# Use Maskey

An entry is `<namespace>/<NAME>`. The name becomes the environment variable, and the namespace groups entries that a command can receive together; it is not a separate vault. For an SSH Key entry, the variable holds the public key, not the private key.

## Manage entries

In the window, click + or choose File > New Entry and enter a namespace, a name such as `GITHUB_TOKEN`, and the value. The namespace chosen in the sidebar is filled in. Kind is suggested from the name until you choose one; SSH Key changes how `mk run` supplies the key, while the other kinds describe the value and set its icon. Notes are plain text shown with the entry, so keep secrets in the value. Search (⌘F) matches entry and namespace names across every namespace, including while you edit; Escape in the search field clears the query.

The window asks what to do with unsaved changes before switching entries or namespaces, closing, or quitting. You can save when the form is valid, discard, or continue editing. Cancel and Escape in the editor ask before discarding changes without offering Save; an unchanged form closes at once.

The value editor accepts multiple lines but refuses to paste CRLF line endings, which it would change to LF. An existing value containing carriage returns stays unchanged when you edit the entry; replace it with `mk set --stdin` to preserve its line endings. The window does not remove a private key's passphrase, so it refuses a protected key for an SSH Key entry; import that key from your terminal with `mk set --stdin`, which removes the passphrase, into an SSH Key entry, such as a new one whose name ends in `SSH_KEY`; pipe an encrypted PKCS#8 key through `openssl pkey -in <file>` first, as the window explains.

You can reveal or edit a value in the window without granting program access. Copy Value keeps it on this Mac, marks it as confidential for clipboard managers, and clears it after 90 seconds or when Maskey quits, unless something else was copied since. Text selected in a revealed value and copied is ordinary text.

From a terminal, `mk set` prompts without echoing the value and asks before replacing an existing one. `mk set --stdin` reads from a pipe or file and replaces without asking; a terminal echoes what you type to it. A one-line value loses its final line ending, while a value of several lines is stored byte for byte unless `mk` removes an SSH key's passphrase. An entry of another kind than SSH Key keeps a protected private key as it is, for a program that reads the passphrase separately, and `mk set` warns.

`mk list` shows entry references, and `mk list --json` adds kind, modification time, and program access, without values or notes. Names are case-sensitive; `mk` warns before creating a name that differs from an existing one only in case. `mk rename` moves an entry with its value and details. Deleting an entry in the window or with `mk delete` removes it from every Mac syncing this Keychain and cannot be undone.

## Run a command

Give a command one entry or a whole namespace:

```sh
mk run github/GITHUB_TOKEN -- gh pr list
mk run github -- gh auth status
```

Each selected entry sets a variable of the same name, replacing an inherited one. You can combine namespaces and individual entries; an entry you name overrides a namespace's entry of that name. `mk run` starts the command without a shell, so use `sh -c '…'` for pipes or variable expansion in the child. Keep secret values out of command arguments.

Interactive and full-screen programs work when `mk run` runs in the foreground of the terminal that is its input and output; Ctrl-Z suspends the command and `fg` resumes it. Captured output or selected values containing control characters use pipes instead.

`mk --help` and `mk <command> --help` describe command syntax, flags, and examples. For scripts, `mk help exit-codes` describes error kinds, exit statuses, JSON output, and how to handle partial or uncertain writes.

## SSH keys

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

## Program access

`mk run` needs no Program access grant. Turn on an entry's Program access switch only for a program that must obtain its raw value from a helper command, such as `mk get <namespace>/<NAME>`. Configure that program to capture the output itself; `mk get` refuses terminal stdout. New entries start without program access, and a grant applies to any program that can run `mk` with access to the Keychain, not just the intended consumer.

The switch and `mk grant` or `mk revoke` require your login password, Touch ID, or Apple Watch. The commands use the Keychain service installed by `mk setup`; interrupting them while the approval dialog is open withdraws the change. One approval covers further changes for 5 minutes, with the service and the window each keeping their own approval. Replacing, editing, or renaming an entry preserves its program access.

## Activity

`mk activity` shows which programs got, read, wrote, deleted, or changed program access to which entries on this Mac, grouped over the last week unless you ask for more. Each record names the program that ran `mk` and the programs that started it, or Maskey for the window; refused grants and revokes are recorded too. For an SSH key, the read at the start of `mk run` is recorded, but individual signatures are not.

The record never contains values, notes, command arguments, or environment variables. Maskey keeps the current month's file and the 12 before it in `~/Library/Logs/io.github.entwining.maskey`, removing older ones when it writes a new month's first record.

## Troubleshooting

`mk doctor` checks Apple's interpreter, Keychain access, the service, and the skill links. It reads entry attributes, not values. It cannot check iCloud Keychain sync: Maskey sees only this Mac's Keychain, and iCloud does not report when a change reaches another Mac.

- `unavailable`: neither the Keychain nor the service is reachable. Allow the socket under [Sandboxed agents](security.md#sandboxed-agents), or run `mk setup` in a normal terminal if `mk doctor` shows the service is not running.
- `uncertain`: a write may have happened. Check the entry in Maskey before repeating it; a list of names cannot show whether a new value was saved.
- `locked`: unlock the Mac.

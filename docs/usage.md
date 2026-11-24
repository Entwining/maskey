# Use Maskey

An entry is `<namespace>/<NAME>`. The name becomes the environment variable, and the namespace groups entries that a command can receive together; it is not a separate vault. For an SSH Key entry, the variable holds the public key, not the private key.

## Manage entries

In the window, click + or choose File > New Entry. The form uses the selected namespace and its store. With no namespace selected, it uses the store shown alone or the default when all stores are shown. With more than one store on this Mac, check Store. Then enter the namespace, a name such as `GITHUB_TOKEN`, and the value. Kind is suggested from the name until you choose one. SSH Key changes how `mk run` supplies the key. Other kinds describe the value and set its icon. Notes appear with the entry, so keep secrets in the value.

Search (⌘F) matches entry and namespace names across the stores currently shown, including while you edit. Escape in the search field clears the query.

The window asks what to do with unsaved changes before switching entries or namespaces, closing, or quitting. You can save when the form is valid, discard, or continue editing. Cancel and Escape in the editor ask before discarding changes without offering Save; an unchanged form closes at once.

The value editor accepts multiple lines but refuses a CRLF paste because it would change the line endings. Editing an entry whose value already contains carriage returns preserves that value. To replace it from a file, use the Terminal command shown in the form. It selects the store shown in the form and keeps the line endings of a value of several lines. In New Entry, the command names the reference you typed only when it is valid and the store's entries have been read and show none of that name. Fill in any placeholder so the reference names an unused entry, because `mk set --stdin` replaces an existing value without asking. `mk set` gives a new entry the kind its name suggests and keeps an existing entry's kind: for an SSH Key, keep the suggested `SSH_KEY` suffix so `mk set` imports it as that kind, and for another kind, set Kind in the window afterwards when the form says so. For a passphrase-protected key, [import the key from your terminal](#ssh-keys).

Choose Show Value to reveal an entry without granting program access. Copy Value keeps it on this Mac, marks it as confidential for clipboard managers, and clears it after 90 seconds or when Maskey quits, unless something else was copied since. Text selected in a revealed value and copied is ordinary text.

In a terminal, `mk set <namespace>/<NAME>` hides single-line input and asks before replacing an existing value. For multiline input, use the window or supply a pipe or file to `mk set --stdin`. This replaces without asking, and a terminal echoes anything typed into stdin. A single-line value loses its trailing LF or CRLF. Accepted multiline input keeps its bytes unless an SSH key import removes a passphrase. Empty values and NUL bytes are rejected, and Bitwarden values must be UTF-8.

`mk list` shows references. `mk list --json` adds kind, program access, and modification time when known. Names are case-sensitive. Both `mk set` and the window's Name field warn if a new reference differs from one in the same store only in case. `mk rename` moves an entry within its store with its value and details.

The window's Delete confirmation names the entry and its store. Deleting an iCloud entry removes it from every Mac syncing that Keychain. Deleting a Bitwarden entry moves it to Bitwarden's trash and removes it from the shared project for everyone. Maskey cannot undo either deletion.

## Run a command

Give a command one entry or a whole namespace:

```sh
mk run github/GITHUB_TOKEN -- gh pr list
mk run github -- gh auth status
```

Each selected entry sets a variable of the same name, replacing an inherited one. An explicit entry overrides namespace entries with that name. If two namespaces still supply the same name, or two distinct explicit entries have that name, `mk run` reports `conflict` before starting the command. Put `mk` flags before the first selector. The command starts without a shell, so use `sh -c '…'` for pipes or variable expansion in the child. Keep secret values out of command arguments.

Interactive and full-screen programs can use a terminal when stdin and stdout are foreground terminals. Ctrl-Z suspends the command and `fg` resumes it. If stdin or stdout is not a foreground terminal, or an ordinary credential contains a control character, `mk run` uses pipes. Redirecting only stderr leaves the child's terminal available.

`mk --help` and `mk <command> --help` describe command syntax, flags, and examples. For scripts, `mk help exit-codes` describes error kinds, exit statuses, JSON output, and how to handle partial or uncertain writes.

## SSH keys

Choose SSH Key (`ssh`) for an unprotected private key used by OpenSSH. A new entry whose name ends in `SSH_KEY` gets this kind automatically. `mk set` preserves an existing entry's kind. Private Key (`pem`) supplies the key as a variable for programs that read it themselves and can keep a protected key for a program that takes the passphrase separately.

Import a key from your terminal, then run the command that needs it:

```sh
mk set --stdin deploy/DEPLOY_SSH_KEY < /path/to/private-key
mk run deploy/DEPLOY_SSH_KEY -- ssh user@host
```

For a supported passphrase-protected key, `mk set` prompts on the terminal and stores the key without the passphrase or comment, in the same format. For encrypted PKCS#8, pipe `openssl pkey -in <file>` into `mk set --store <name> --stdin deploy/DEPLOY_SSH_KEY` and enter the passphrase at OpenSSL's prompt. Import into a new `*_SSH_KEY` entry or an existing SSH Key entry. The source file stays intact. If `mk` cannot decrypt the key or receives a public key, it reports the problem before writing the entry.

`mk run` sets `DEPLOY_SSH_KEY` to the public key in `authorized_keys` form and `SSH_AUTH_SOCK` to its own SSH agent. The private key does not enter the command's environment or a temporary file. The agent works with `ssh`, Git over SSH, and `ssh-keygen -Y sign`; it ends with the command, closing open agent connections too.

For [SSH commit signing](https://git-scm.com/docs/git-config#Documentation/git-config.txt-usersigningKey) with a key stored as `signing/SIGNING_SSH_KEY`, give Git the public key from the variable:

```sh
mk run signing/SIGNING_SSH_KEY -- sh -c 'git -c gpg.format=ssh -c user.signingKey="key::$SIGNING_SSH_KEY" commit -S -m "<message>"'
```

Select only the keys the command needs, since a server counts each offered key as an authentication attempt. An [`IdentityAgent` setting](https://man.openbsd.org/ssh_config#IdentityAgent) in `ssh_config` takes precedence over `SSH_AUTH_SOCK`; pass `ssh -o IdentityAgent=SSH_AUTH_SOCK`, or `git -c core.sshCommand='ssh -o IdentityAgent=SSH_AUTH_SOCK'`, to use Maskey's agent. Agent forwarding with `ssh -A` lends the agent to the remote host for the run, allowing it to request signatures too.

## Program access

`mk run` needs no Program access grant. Turn on an entry's Program access switch only for a consumer that needs its raw value. Configure the consumer to capture `mk get --store <name> <namespace>/<NAME>` itself, naming the store so that a change of default does not give it another credential. `mk get` refuses terminal stdout. Even with `--json`, success writes the raw value without an added newline. A grant applies to any program that can run `mk get` and reach the store, including through the service. New entries start without a grant.

A change requires macOS owner authentication with your login password, Touch ID, or Apple Watch. An approval can be reused for five minutes. The CLI, the window's iCloud changes, and the window's Bitwarden changes each keep a separate approval. An entry already in the requested state needs none. Interrupting `mk grant` or `mk revoke` withdraws a pending approval. After approval, interrupting the client does not cancel the write. Replacing, editing, or renaming an entry preserves its grant.

## Stores

iCloud Keychain is built in as `icloud`. To share entries across Apple Accounts, [set up the Maskey service](install.md#set-up-the-command-line) on each Mac and connect it to the same [Bitwarden Secrets Manager project](https://bitwarden.com/products/secrets-manager/). Give each Mac its own machine account with read and write access to that project so you can revoke one Mac independently. Create an access token for the account. In Maskey > Settings…, choose Add Bitwarden Store…, enter the token and select the region. You can also connect from a terminal and create an entry in that store.

```sh
mk store add bitwarden
mk set --store bitwarden github/GITHUB_TOKEN
```

`mk store add bitwarden` uses `bitwarden` as the store name unless you pass `--name`. For the EU cloud, pass `--region eu`. If the account can read several projects, choose the project by name or ID in Settings or pass `--project` on the command line. The service checks access before saving the token and reuses the connection for the CLI and window.

Adding a store leaves the default unchanged. Entry commands use this Mac's default, initially `icloud`, unless you pass `--store <name>`. An empty `--store` is a `usage` error. `mk store list` lists the names. Choose Make Default in Settings or run `mk store default <name>` to change the default. Pin the store in a persistent helper, for example `mk get --store bitwarden github/GITHUB_TOKEN`. Otherwise a change of default can select a different credential with the same reference, or fail with `not_found`.

When several stores are connected, use the Stores Shown button at the end of the sidebar's first item to show one store or All Stores. Your chosen namespace and entry stay selected while their store is shown. All Stores groups namespaces and search results by store. To add an empty store's first entry, choose it in the New Entry form.

In Bitwarden, `github/GITHUB_TOKEN#token` is the secret for the Token entry `github/GITHUB_TOKEN`. Its note holds the entry's notes. Secrets without a valid Maskey reference and kind are ignored. Two secrets naming the same entry cause `conflict` until one is renamed or deleted in Bitwarden. When two Macs change the same entry, the later write replaces the earlier one.

Reads can reuse a snapshot less than five minutes old or fall back to [the saved copy during an outage](security.md#bitwarden-stores). Choose Sync Now in Settings or run `mk store sync <name>` for a fresh copy on this Mac. After an upgrade that changes the snapshot format, each Mac needs one successful refresh per store before it can read offline. Creating, editing, renaming, and deleting Bitwarden entries require a live connection. These writes are never queued. Program access belongs to this Mac and can be changed offline for a cached entry.

To rotate a token, choose Replace Access Token… in the store's Settings menu or run `mk store token <name>`. The new token must reach the same project. The store keeps its name and grants. Both `mk store add` and `mk store token` check local settings before reading a token. They prompt with input hidden when stdin is a terminal. From a pipe or file, pass `--stdin`.

`mk store remove <name>` asks before disconnecting the project from this Mac. Without terminal stdin, pass `--yes` or `-y`. Removal deletes the token, local grants, and saved copy. Entries stay in Bitwarden. Removing the default makes `icloud` the default. If removal stops after deleting the token, the store stays listed so you can run the removal again.

The token prompt and removal confirmation apply to the connection selected before they opened. If the name now points to another project, the command reports `conflict`. Settings binds Replace Access Token… and Remove… to the displayed connection too. `mk store token --stdin` and `mk store remove --yes` act on the current connection under the given name.

With `--json`, a successful `mk store add`, `remove`, `token`, `default`, or `sync` returns `store` (the name) and `action` (`added`, `removed`, `token_replaced`, `default_changed`, or `synced`, respectively). `add` also returns the connection details in `connection`.

## Activity

`mk activity` groups this Mac's recorded entry operations over the past week by default, including refused grants and revokes. Use `--full` for individual records and the caller information macOS made available. For SSH, the record covers the key read at the start of `mk run`. It does not track individual signatures.

Use `mk activity --store <name>` to filter by the recorded name, even after a store is removed. Records without a store name count as `icloud`. Without the flag, selectors match across stores. Entries appear as `icloud:github/GITHUB_TOKEN`. The record omits values, notes, command arguments, and environment variables. Maskey keeps the current month's file and the 12 before it in `~/Library/Logs/io.github.entwining.maskey`, removing older ones when it writes a new month's first record.

## Troubleshooting

`mk doctor` checks Apple's interpreter, the Keychain, the service, store configuration, Bitwarden stores, and skill links. It refreshes each configured Bitwarden store it can read and reports counts without values. It cannot tell when iCloud syncs a change to another Mac. Read the stdout report even on success. The first failed interpreter, Keychain, store configuration, or default store check determines the exit status. Service, skill, and other store problems appear as warnings.

If a batch of writes stops, keep the writes reported as completed. Address the failed operation before continuing with any that were not attempted.

- `unavailable` means a required Keychain or service path could not be reached. Check the [service socket permission](security.md#sandboxed-agents) or run `mk setup` in a normal terminal if the service is missing.
- `uncertain` means a write may have happened. Follow the command's recovery. For a Bitwarden entry, check current state in Bitwarden or successfully refresh before checking the entry in Maskey. A list of names cannot confirm a value replacement.
- `locked` means the Keychain needs you to unlock the Mac.
- `network` means Bitwarden could not be reached. Retry the failed operation once online.
- For `unauthenticated`, check the region and token when connecting a new store. For an existing store, replace its missing or refused token. For `permission_denied`, check the machine account's access to the project and the requested operation.
- For `rate_limited` or `remote_error`, wait before retrying the failed operation.

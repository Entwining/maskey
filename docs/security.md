# What Maskey protects, and what it does not

With `mk run`, you can give a tool credentials without putting them in prompts or project files. It masks exact matches for ordinary credentials in the command's stdout and stderr. The command still receives ordinary credentials and can do anything with them. Masking does not catch a value the command encodes or transforms, writes to a file, or sends over the network. A terminal program that inserts line breaks or styling within a value can also show it unmasked. Use commands you trust, and wrap the tool that needs the credential rather than a whole coding agent, whose own logs `mk` cannot mask.

For an SSH Key entry, `mk run` reads the private key into its own process and gives the command a public key and an agent that can sign with the private key. During the run the command can request any signature, for any host or purpose, without further approval. The private key remains in the entry's store and is not hardware-bound. The window can reveal, copy, or edit it, and granting Program access lets `mk get` return it.

Maskey reaches iCloud Keychain through Apple's `/usr/bin/python3`. Another program running as you under that interpreter can access the same items if it can reach the Keychain, bypassing Maskey's program access checks and activity record. A program running as you can also alter the record if it can write to the record's directory. Keychain items remain accessible after this Mac's first unlock since startup, so unattended jobs can use them while the screen is locked.

## Bitwarden stores

A Bitwarden access token has its machine account's permissions, which can extend to several projects. Limit the account to the project you intend to share. Selecting a project in Maskey limits the store, while Bitwarden enforces the account's permissions. Maskey keeps the token in a non-syncing Keychain item on this Mac and uses it from the Maskey service. The service encrypts entry names, values, and notes with the organization key before sending them to Bitwarden.

For offline reads, the service saves only the project's Maskey entries in `~/Library/Application Support/io.github.entwining.maskey/stores`. It seals them with the organization key and uses the store's access token to unlock the copy of that key saved with them. The snapshot has no offline expiration. A remotely deleted entry can remain readable until a successful refresh. A network failure, rate limit, or server error can leave an older copy in use even after connectivity returns. A refresh that reports invalid credentials, denied access, or a missing project makes the service discard that copy.

Program access for a Bitwarden entry is kept in this Mac's Keychain. Someone who can read the project directly in Bitwarden can read the value regardless of that local grant. Removing the store in Maskey removes its token, grants, and saved copy from this Mac.

## Sandboxed agents

Bitwarden entry operations, including offline reads, and `mk grant` and `mk revoke` require the per-user Maskey service. A sandbox can also use it for iCloud when direct Keychain access is blocked. Allow its Unix socket in the sandbox's network permissions.

```text
~/Library/Application Support/io.github.entwining.maskey/service.sock
```

`mk` reads which stores this Mac has from `stores.json` beside the socket. Where a sandbox keeps it from reading that file, the service reads it for `mk` over the socket, so the sandbox needs no read permission for it. If neither path can read the configuration, `--store icloud` can still select iCloud explicitly.

For ordinary iCloud entry operations, `mk` tries the Keychain directly where allowed. When it cannot write the activity record, it asks the service to perform and record those operations first. If the service is unavailable, direct iCloud access can still succeed, with a warning if the operation could not be recorded. Keep `~/Library/Logs/io.github.entwining.maskey` outside the sandbox's writable paths so sandboxed processes cannot rewrite or delete the record.

Both peers verify the user ID and signed Maskey build before exchanging a request or value. The command and the masking of its output stay in the caller's sandbox.

For SSH Key entries, `mk run` creates a separate agent socket in a private directory under the system temporary directory, which you can set with `TMPDIR`. The sandbox must allow `mk` to create a Unix socket there and the command to connect to it. The socket lasts only for the run.

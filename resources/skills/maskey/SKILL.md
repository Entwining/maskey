---
name: maskey
description: Give commands API keys, tokens, passwords, SSH keys, and other credentials that the user keeps in Maskey (iCloud Keychain), through its mk command line, without the values entering the conversation; also store, rename, list, or delete those credentials. Use when a task needs a credential or environment variable the user has not put in the environment, mentions Maskey or mk, or a command fails for a missing token.
---

# Maskey

The owner, as `mk` help and hints call them, is the user you work for: their iCloud Keychain holds the credentials, and only they can approve a change to program access. `mk run` hands credentials from that Keychain to commands as environment variables and masks their exact values in stdout and stderr. Never ask the owner to paste a credential into the conversation, put one in a command line, or write one to a file.

Syntax is in `mk --help` and `mk <command> --help`; error kinds and exit statuses in `mk help exit-codes`. An error with a recovery ends with it, a `mk: hint:` line or `recovery` with `--json`; follow it rather than retrying. Add `--json` for structured output.

## Use a credential

1. `mk list` prints entries as `<namespace>/<NAME>`, one per line; `mk list <namespace>` prints one namespace. The name is the environment variable the command receives.
2. Wrap the command that needs the credential, not a shell you keep or a whole agent, and give it only the entries it needs: `mk run github/GITHUB_TOKEN -- gh pr list` sets that entry, and `mk run github -- gh auth status` every entry of the namespace. List several selectors before `--`; an entry you name wins over the same NAME from a namespace.
3. `mk run` starts the command without a shell. When it needs a variable expanded, quote the script in single quotes so the variable expands in the child, where it is set, and pass the value on stdin rather than in an argument, which other processes can see: `mk run api/API_TOKEN -- sh -c 'printf "Authorization: Bearer %s\n" "$API_TOKEN" | curl -H @- https://api.example.com'`. In double quotes your own shell expands it first, where it is unset or holds something else.
4. The command's output shows each exact value as a marker such as `***`. Masking covers stdout and stderr only, so do not run commands that print values in another form (base64, URL-encoded, hex dumps, debug logs) or insert line breaks or styling within them on a terminal.

A program configured to run a command for its key, such as Claude Code's `apiKeyHelper`, can run `mk get <namespace>/<NAME>`, which writes the raw value to its stdout. Configure the consumer to run it; never run `mk get` yourself, since its output would enter the conversation. If it fails with `not_granted`, the owner has not granted the entry program access: use `mk run`, and ask the owner to grant it only when a program must read the value from stdout. Never run `mk grant` or `mk revoke` yourself: within 5 minutes of the owner's last approval they change access without asking again.

`mk list --json` gives each entry's kind and whether it has program access.

An `ssh` entry is an SSH private key the command never receives: `mk run` sets its NAME to the public key and signs with the private key through an agent at `SSH_AUTH_SOCK` until the command ends. Wrap the command that connects or signs, such as `mk run deploy/DEPLOY_SSH_KEY -- git push`, and select only the keys it needs, since a server counts each key offered as one authentication attempt. Do not add `-A`, which lends the keys to the remote host. When `ssh_config` sets `IdentityAgent`, which takes precedence, pass `-o IdentityAgent=SSH_AUTH_SOCK` to `ssh`, or `-c core.sshCommand='ssh -o IdentityAgent=SSH_AUTH_SOCK'` to `git`, whose own `-o` means something else. For an SSH commit signature, give git the public key rather than a key file: `mk run signing/SIGNING_SSH_KEY -- sh -c 'git -c gpg.format=ssh -c user.signingKey="key::$SIGNING_SSH_KEY" commit -S -m "<message>"'`. If `mk run` reports that the key is protected by a passphrase, ask the owner to run `mk set --stdin <namespace>/<NAME> < <file>` in their terminal, which stores it without the passphrase.

A `pem` entry is a private key a program reads itself, as a variable or on stdin inside `sh -c`. An SSH key stored before kind `ssh` existed has kind `pem`; when `ssh` or `git` needs it, ask the owner to choose SSH Key for it in the Maskey window rather than loading it into an agent yourself. If the program accepts only a file path, tell the owner instead of writing the key to disk. A program that reads the key's passphrase from a variable, such as Snowflake's `PRIVATE_KEY_PASSPHRASE`, takes it from another entry of the namespace.

When the owner asks what used a credential, `mk activity` shows which programs got, read, or changed which entries on this Mac, without values.

## Store or change a credential

- The owner enters a value themselves: ask them to run `mk set <namespace>/<NAME>` in their own terminal, where the prompt hides the input and `mk` asks before replacing an existing value, or to add it in the Maskey window.
- A value another program prints: `producer | mk set --stdin <namespace>/<NAME>`, which replaces an existing value without asking. A one-line value loses its final line ending; a value of several lines, such as a PEM key, is stored byte for byte. A new entry whose name ends in `SSH_KEY` gets kind `ssh`; only the owner changes an existing entry's kind, in the Maskey window.
- Names are case-sensitive: `github/github_token` is a second entry beside `github/GITHUB_TOKEN`, and `mk` warns before creating it. Use the exact name `mk list` prints.
- `mk rename old/NAME new/NAME` moves an entry with its value, kind, notes, and program access. `mk delete --yes <namespace>/<NAME>...` deletes entries from every Mac on the owner's Apple Account; run it only for entries the owner asked to delete.
- `mk uninstall --yes` stops the Keychain service and removes Maskey's links and records from this Mac, leaving entries and Maskey.app; run it only when the owner asks to uninstall Maskey.


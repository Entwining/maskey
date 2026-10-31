---
name: maskey
description: Give commands API keys, tokens, passwords, and other credentials that the user keeps in Maskey (iCloud Keychain), through its mk command line, without the values entering the conversation; also store, rename, list, or delete those credentials. Use when a task needs a credential or environment variable the user has not put in the environment, mentions Maskey or mk, or a command fails for a missing token.
---

# Maskey

`mk run` hands credentials from the user's iCloud Keychain to commands as environment variables and masks their exact values in stdout and stderr. Never ask the user to paste a credential into the conversation, put one in a command line, or write one to a file.

Syntax is in `mk --help` and `mk <command> --help`; error kinds and exit statuses in `mk help exit-codes`. An error with a recovery ends with it, a `mk: hint:` line or `recovery` with `--json`; follow it rather than retrying. Add `--json` for structured output.

## Use a credential

1. `mk list` prints entries as `<namespace>/<NAME>`, one per line; `mk list <namespace>` prints one namespace. The name is the environment variable the command receives.
2. Wrap the command that needs the credential, not a shell you keep or a whole agent, and give it only the entries it needs: `mk run github/GITHUB_TOKEN -- gh pr list` sets that entry, and `mk run github -- gh auth status` every entry of the namespace. List several selectors before `--`; an entry you name wins over the same NAME from a namespace.
3. `mk run` starts the command without a shell. When it needs a variable expanded, quote the script in single quotes so the variable expands in the child, where it is set, and pass the value on stdin rather than in an argument, which other processes can see: `mk run api/API_TOKEN -- sh -c 'printf "Authorization: Bearer %s\n" "$API_TOKEN" | curl -H @- https://api.example.com'`. In double quotes your own shell expands it first, where it is unset or holds something else.
4. The command's output shows each exact value as a marker such as `***`. Masking covers stdout and stderr only, so do not run commands that print values in another form (base64, URL-encoded, hex dumps, debug logs) or insert line breaks or styling within them on a terminal.

A program configured to run a command for its key, such as Claude Code's `apiKeyHelper`, can run `mk get <namespace>/<NAME>`, which writes the raw value to its stdout. Configure the consumer to run it; never run `mk get` yourself, since its output would enter the conversation. If it fails with `not_granted`, the owner has not granted the entry program access: use `mk run`, and ask the owner to grant it only when a program must read the value from stdout. Never run `mk grant` or `mk revoke` yourself: within 5 minutes of the owner's last approval they change access without asking again.

`mk list --json` gives each entry's kind and whether it has program access. A `pem` entry is a private key: hand it to the tool on stdin inside `sh -c`, such as `sh -c 'printf "%s\n" "$DEPLOY_KEY" | ssh-add -'`. If the tool accepts only a file path, tell the user instead of writing the key to disk.

When the user asks what used a credential, `mk activity` shows which programs got, read, or changed which entries on this Mac, without values.

## Store or change a credential

- The user enters a value themselves: ask them to run `mk set <namespace>/<NAME>` in their own terminal, where the prompt hides the input, or to add it in the Maskey window.
- A value another program prints: `producer | mk set --stdin <namespace>/<NAME>`. A one-line value loses its final line ending; a value of several lines, such as a PEM key, is stored byte for byte.
- `mk rename old/NAME new/NAME` moves an entry with its value, kind, notes, and program access. `mk delete --yes <namespace>/<NAME>...` deletes entries from every Mac on the user's Apple Account; run it only for entries the user asked to delete.


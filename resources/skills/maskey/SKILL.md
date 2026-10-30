---
name: maskey
description: Give commands API keys, tokens, passwords, and other credentials that the user keeps in Maskey (iCloud Keychain), through its mk command line, without the values entering the conversation; also store, rename, list, or delete those credentials. Use when a task needs a credential or environment variable the user has not put in the environment, mentions Maskey or mk, or a command fails for a missing token.
---

# Maskey

`mk` keeps credentials in the user's iCloud Keychain and hands them to commands as environment variables. Nothing prints a stored value: there is no get or export, and a value never appears in an argument, file, or message from `mk`. Keep it that way on your side: never ask the user to paste a credential into the conversation, never put one in a command line, and never write one to a file.

Syntax is in `mk --help` and `mk <command> --help`; error kinds and exit statuses in `mk help exit-codes`. An error with a recovery ends with it, a `mk: hint:` line or `recovery` with `--json`; follow it rather than retrying. Add `--json` for structured output.

## Use a credential

1. `mk list` prints entries as `<namespace>/<NAME>`, one per line; `mk list <namespace>` prints one namespace. The name is the environment variable the command receives.
2. Wrap the command that needs the credential, not a shell you keep or a whole agent, and give it only the entries it needs: `mk run -e github/GITHUB_TOKEN -- gh pr list` sets that entry, and `-n github` every entry of the namespace. Repeat either option for more.
3. `mk run` starts the command without a shell. When it needs a variable expanded, quote the script in single quotes so the variable expands in the child, where it is set: `mk run -e api/API_TOKEN -- sh -c 'curl -H "Authorization: Bearer $API_TOKEN" https://api.example.com'`. In double quotes your own shell expands it first, where it is unset or holds something else.
4. The command's output shows each value as a marker such as `***`. Masking covers exact values on stdout and stderr only, so do not run commands that print values in another form (base64, URL-encoded, hex dumps, debug logs).

`mk list --json` gives each entry's kind. A `pem` entry is a private key: hand it to the tool on stdin inside `sh -c`, such as `sh -c 'printf "%s\n" "$DEPLOY_KEY" | ssh-add -'`. If the tool accepts only a file path, tell the user instead of writing the key to disk.

## Store or change a credential

- The user enters a value themselves: ask them to run `mk set <namespace>/<NAME>` in their own terminal, where the prompt hides the input, or to add it in the Maskey window.
- A value another program prints: `printf '%s' "$(producer)" | mk set --stdin <namespace>/<NAME>`. `mk` stores input byte for byte, so the command substitution is what drops the producer's trailing newline.
- `mk rename old/NAME new/NAME` moves an entry with its value, kind, and notes. `mk delete --yes <entry>` deletes it from every Mac on the user's Apple Account; run it only when the user asked for that deletion.


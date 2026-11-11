<p align="center">
  <img src="resources/icon.png" alt="" width="128">
</p>
<h1 align="center">Maskey</h1>

Maskey stores API keys, tokens, passwords, and SSH keys in iCloud Keychain for scripts and coding agents. It has a macOS window and the `mk` command line. Entries sync across your Macs without another account or hosted service.

## What it does

### Give a command its credentials

[`mk run`](docs/usage.md#run-a-command) supplies selected entries as environment variables. It masks their exact values in stdout and stderr. No bootstrap token is needed.

```sh
mk set github/GITHUB_TOKEN      # prompts without echoing the value
mk run github -- gh auth status
```

### Lend an SSH key

For an [SSH Key entry](docs/usage.md#ssh-keys), the command gets a public key and an SSH agent. The private key stays in `mk`'s process. During the run, the command can request any signature with that key. The agent ends with the command.

### Manage entries in a window

[Create, search, and edit entries](docs/usage.md#manage-entries) in the macOS window. Reveal or copy a value when you need it. [Program access](docs/usage.md#program-access) lets a credential helper obtain a raw value after you grant the entry access.

### Know which programs used them

[`mk activity`](docs/usage.md#activity) shows which programs used or changed entries through Maskey on this Mac. The record contains no values, notes, command arguments, or environment variables.

## Get started

You need an Apple silicon Mac and Apple's Command Line Tools or Xcode.

```sh
brew install --cask entwining/tap/maskey
xattr -dr com.apple.quarantine /Applications/Maskey.app
mk setup
```

Maskey is signed ad hoc and is not notarized. The `xattr` command removes Gatekeeper's quarantine. You can also [install with a script or build from source](docs/install.md).

For coding agents, run `mk skill install` to install the [shipped skill](resources/skills/maskey/SKILL.md). Sandboxes need permission to connect to the [Keychain service's socket](docs/security.md#sandboxed-agents).

## Know the limits

Use commands you trust. Wrap the tool that needs the credential, not a whole coding agent, whose own logs `mk` cannot mask. Masking cannot protect credentials a command transforms, saves, or sends elsewhere. Other programs hosted by [Apple's Python can access the same Keychain items](docs/security.md). The activity record can also be altered by programs running as you.

## Development

Maskey is written in Go with [MyGo](https://github.com/egoist/mygo). Run the [repository checks](AGENTS.md#checks) before submitting a change.

<p align="center">
  <img src="resources/icon.png" alt="" width="128">
</p>
<h1 align="center">Maskey</h1>

Maskey is a macOS app and command line (`mk`) that let scripts and coding agents use credentials without putting their values in prompts or project files. Keep entries in iCloud Keychain to sync them across Macs on the same Apple Account, or [connect a Bitwarden Secrets Manager project](docs/usage.md#stores) to share them across accounts.

## What it does

### Give a command its credentials

[`mk run`](docs/usage.md#run-a-command) gives a command just the API keys, tokens, or passwords you select, as environment variables. If the command prints an exact value to stdout or stderr, `mk` shows a marker such as `***`.

```sh
mk set github/GITHUB_TOKEN      # prompts without echoing the value
mk run github/GITHUB_TOKEN -- gh auth status
```

### Lend an SSH key

For an [SSH Key entry](docs/usage.md#ssh-keys), the command gets a public key and an SSH agent. The private key stays in `mk`'s process. During the run, the command can request any signature with that key. The agent ends with the command.

### Manage entries in a window

[Create, search, and edit entries](docs/usage.md#manage-entries) in the macOS window. Reveal or copy a value when you need it. [Program access](docs/usage.md#program-access) lets a credential helper obtain a raw value after you grant the entry access.

### Know which programs used them

[`mk activity`](docs/usage.md#activity) shows which programs used or changed entries through Maskey on this Mac. The record contains no values, notes, command arguments, or environment variables.

## Get started

You need an Apple silicon Mac running macOS 13 or later and Apple's Command Line Tools or Xcode.

```sh
brew install --cask entwining/tap/maskey
xattr -dr com.apple.quarantine /Applications/Maskey.app
mk setup
```

Maskey is signed ad hoc and is not notarized. The `xattr` command removes Gatekeeper's quarantine. You can also [install with a script or build from source](docs/install.md).

For coding agents, run `mk skill install` to install the [shipped skill](resources/skills/maskey/SKILL.md). Sandboxed commands that use the Maskey service need access to [its Unix socket](docs/security.md#sandboxed-agents).

## Know the limits

Wrap only the tool that needs the credential. A whole coding agent can write logs outside `mk`'s output. Give credentials only to commands you trust with them, since a command can [transform, save, or send a value beyond Maskey's masking](docs/security.md).

## Development

Maskey is written in Go with [MyGo](https://github.com/egoist/mygo). Run the [repository checks](AGENTS.md#checks) before submitting a change.

## License

Maskey is available under the [MIT license](LICENSE).

# Install and maintain Maskey

Maskey requires an Apple silicon Mac running macOS 13 or later. Apple's Command Line Tools (`xcode-select --install`) or Xcode provide the Python interpreter Maskey uses at `/usr/bin/python3`. To sync iCloud entries, turn on iCloud Keychain on each Mac using the same Apple Account. Macs on different Apple Accounts can [connect to the same Bitwarden project](usage.md#stores).

## Homebrew

```sh
brew install --cask entwining/tap/maskey
```

This puts Maskey in `/Applications` and `mk` on your `PATH`. Maskey is signed ad hoc, without a Developer ID, and is not notarized, so Gatekeeper blocks the app Homebrew downloaded. Remove its quarantine attribute:

```sh
xattr -dr com.apple.quarantine /Applications/Maskey.app
```

After a Homebrew upgrade, run the `xattr` command again for the newly installed app. To leave routine updates to Maskey, set `HOMEBREW_NO_UPGRADE_AUTO_UPDATES_CASKS=1` for `brew upgrade`.

## Install script

```sh
curl -fsSL https://github.com/Entwining/maskey/releases/latest/download/install.sh | sh
```

The script downloads the latest archive and verifies its signature with Maskey's update key. It replaces an existing installation in `/Applications` or `~/Applications`. For a new installation it uses `/Applications` if writable and `~/Applications` otherwise. It then tries to open Maskey. The `curl` download carries no quarantine attribute.

Verify a release archive's [build provenance attestation](https://cli.github.com/manual/gh_attestation_verify) with `gh attestation verify maskey-<version>-darwin-arm64.tar.gz --repo Entwining/maskey`.

## Build from source

With Go at the version in [go.mod](../go.mod) or later, run in a checkout:

```sh
go tool mygo build -skip-dmg
ditto build/darwin-arm64/Maskey.app /Applications/Maskey.app
```

## Set up the command line

With Homebrew, run `mk setup` in a normal terminal. Homebrew supplies the `mk` link. For a script or source install, choose Maskey > Install Command Line Tool… in the app to link `~/.local/bin/mk` to it. Add `~/.local/bin` to your `PATH` if needed. Both paths install the per-user Maskey service, which starts at login without opening a window.

For coding agents, `mk skill install` links the [shipped skill](../resources/skills/maskey/SKILL.md) into `~/.claude/skills` and `~/.agents/skills`. The links point into the app, so the skill matches the installed `mk`. Use `mk skill show` for an agent that reads skills from elsewhere. Allow the [service socket](security.md#sandboxed-agents) for Bitwarden, CLI changes to program access, and iCloud requests that need the service.

## Upgrade

Maskey can install signed updates, including over a source build, when it can write to its installation directory. With automatic checks enabled, it checks shortly after launch and normally once a day while running. Choose Maskey > Check for Updates… to check manually. When an update is available, the menu and sidebar open the update window. The new version runs after Maskey relaunches.

An in-place update keeps entries in their stores and preserves the `mk` and skill links. If the running service rejects the new build, it exits and launchd restarts it. macOS can report a Launch Constraint Violation and delay that restart while `mk` waits. If the service is still unavailable, run `mk setup` in a normal terminal and check that the sandbox allows the [current socket](security.md#sandboxed-agents).

## Uninstall

Entries stay in their stores when you uninstall Maskey. If you want to [delete particular entries](usage.md#manage-entries), do that first. Then quit Maskey, run `mk uninstall` in a normal terminal, and confirm the listed removals.

Maskey removes this Mac's Bitwarden connections, tokens, and program access first. If it cannot read the store configuration or finish removing a store, it stops before removing anything else. Then it stops the Maskey service and removes its configuration, the activity record, the window's saved state, and the `mk` and skill links, and reports a result for each item, including failures. With `--json`, `items` holds those results and `bundle` gives the path to `Maskey.app`. Files a service may still write are kept.

If cleanup fails, keep the app, resolve the reported failure and run uninstall again. The recovery hint gives the executable path if the `mk` link is already gone. After cleanup succeeds, move `Maskey.app` to the Trash or run `brew uninstall --cask maskey` for a Homebrew installation.

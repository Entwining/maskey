# Install and maintain Maskey

You need an Apple silicon Mac and Apple's Command Line Tools (`xcode-select --install`) or Xcode, which provide `/usr/bin/python3`. To use entries on several Macs, turn on iCloud Keychain on each with the same Apple Account and install Maskey on each. Maskey has no sync settings of its own and cannot tell when a change reaches another Mac.

## Homebrew

```sh
brew install --cask entwining/tap/maskey
```

This puts Maskey in `/Applications` and `mk` on your `PATH`. Maskey is signed ad hoc, without a Developer ID, and is not notarized, so Gatekeeper blocks the app Homebrew downloaded. Remove its quarantine attribute:

```sh
xattr -dr com.apple.quarantine /Applications/Maskey.app
```

Maskey then updates itself. `brew upgrade` also replaces it with each new version, which is quarantined again because an ad hoc signature cannot carry your approval to the next version; set `HOMEBREW_NO_UPGRADE_AUTO_UPDATES_CASKS=1` to leave updates to Maskey, or run the `xattr` command again after an upgrade.

## Install script

```sh
curl -fsSL https://github.com/Entwining/maskey/releases/latest/download/install.sh | sh
```

The script downloads the latest version, checks its signature against the key Maskey's updates are signed with, installs it in `/Applications` (or `~/Applications` when it cannot write there), and opens it. `curl` sets no quarantine attribute, so Gatekeeper does not block it.

Verify a release archive's [build provenance attestation](https://cli.github.com/manual/gh_attestation_verify) with `gh attestation verify maskey-<version>-darwin-arm64.tar.gz --repo Entwining/maskey`.

## Build from source

With Go at the version in [go.mod](../go.mod) or later, run in a checkout:

```sh
go tool mygo build -skip-dmg
ditto build/darwin-arm64/Maskey.app /Applications/Maskey.app
```

Like an installed copy, a source build offers to replace itself with each published release.

## Set up the command line

Open Maskey and choose Maskey > Install Command Line Tool. This links `~/.local/bin/mk` to the app and installs a per-user Keychain service that starts at login without opening a window. Add `~/.local/bin` to your `PATH` if your shell cannot find `mk`. For a Homebrew install, run `mk setup` in a normal terminal to install the service; Homebrew already supplies the `mk` link.

For coding agents, `mk skill install` links the [shipped skill](../resources/skills/maskey/SKILL.md), which teaches agents to use credentials without putting their values in the conversation, into `~/.claude/skills` and `~/.agents/skills`. The links point into the app, so the skill matches the installed `mk`; `mk skill show` prints it for an agent that reads skills from elsewhere. Agents whose sandbox blocks Keychain access also need the socket permission described under [Sandboxed agents](security.md#sandboxed-agents).

## Upgrade

Maskey checks for a new version shortly after it opens and then once a day, and Maskey > Check for Updates checks at once. It installs a version only after checking its signature, and the new version runs once you restart Maskey. Entries stay in the Keychain, `mk` and the skill links point into the app, and the Keychain service restarts from the new version the first time the new `mk` reaches it.

Because each version's ad hoc signature differs, macOS can refuse that restart once, write a crash report for Maskey that names a Launch Constraint Violation, and start the new version about 10 seconds later; `mk` waits for it.

## Uninstall

Entries stay in iCloud Keychain, on every Mac, when Maskey is gone; delete them in the window or with `mk delete` first if you no longer want them. Quit Maskey, then run `mk uninstall`: it lists what Maskey installed on this Mac, asks once, stops the Keychain service, and removes its configuration, the activity record, the window's saved state, and the `mk` and skill links. Then move Maskey.app to the Trash, or run `brew uninstall --cask maskey` if Homebrew installed it.

# Maskey

Before changing UI code, MyGo usage, the MyGo version, or the build, read `.agents/skills/mygo-maintenance/SKILL.md`. `mygo install-skills` overwrites that directory, so put Maskey's own rules here, not there.

## Checks

```sh
golangci-lint run
go tool mygo vet .
go test -race ./...
```

Use the golangci-lint version that `.github/workflows/check.yml` pins; `.golangci.yml` owns the linters and the file-length limit. Split a file that outgrows the limit by concern within its package rather than raising the limit.

Tests use the in-memory backend in `internal/store/storetest` and fake interpreters; those that start Apple's interpreter run only the helper's host check or a bare socket client, and none makes a Keychain call. They cannot show live Keychain access, sync between Macs, or the rendered window.

## The real Keychain

The installed app, `mk`, and `keychain.New` with Apple's interpreter act on the user's iCloud Keychain, which syncs to every Mac on their Apple Account. Run them only with the user's authorization for that session, and then only on synthetic entries in a namespace made for the test, deleted at the end. Never list, read, or migrate entries you did not create; `mk doctor` reads every entry's attributes, so it is not a harmless probe either.

Inside a sandbox that blocks the `com.apple.securityd` Mach services, every direct Keychain call fails with OSStatus -25291 (kind `unavailable`), whatever the code under test does, and `mk` falls back to the Keychain service if it can reach its socket. Run live checks where the Keychain is reachable.

## Persistent identity

`store.Service` and `identifier` in `mygo.json` are the same string, which a test checks: the app's bundle identifier and the service attribute (`kSecAttrService`) of every entry. The account is `<namespace>/<NAME>`, the entry's kind is stored in `kSecAttrDescription` as a `store.ValueKind` string, and its notes in `kSecAttrComment`. Changing any of these, or the item's `synchronizable` flag, orphans or misreads existing entries on every synced Mac; it needs a migration the user has approved. Entries use `AfterFirstUnlock` so unattended jobs work while the screen is locked; `ThisDeviceOnly` classes cannot sync.

## Values

No path prints, logs, or returns a stored value outside `mk run` (into the child's environment, masked in its output) and the window's Reveal, Copy, and Edit form. Do not add a get, export, or raw-output option. Values never travel in arguments, temporary files, or error messages: `store.Error` carries none, and the helper's stderr stays unread because Python can quote the request there.

## Keychain helper and service

`internal/keychain` runs the embedded helper as `/usr/bin/python3 -I -S -c` with only `PATH` in its environment, the request on stdin. The absolute path and the bare environment keep another interpreter, or `DEVELOPER_DIR` redirecting Apple's shim, from hosting it without Apple's `com.apple.python3` identity. The ready line printed after the host check is what separates a confirmed failure (`prerequisite`) from a write that may have happened (`uncertain`); keep both outcomes distinct.

`internal/agent` is the per-user Keychain service for sandboxed callers. Clients fall back to it only on `unavailable`, never on `locked`, `cancelled`, or `uncertain`, and a connection that ends after a write was sent is `uncertain`, not a reason to resend. Both peers authenticate as the same signed executable before a request crosses the socket. The service performs Keychain operations only; the command and its masking stay in the caller's sandbox.

## Command line and GUI in one executable

`main` dispatches to the CLI before any GUI work. Nothing on a CLI path may call `mygo.App` or `mygo.NewWindow`, because they create the GUI backend; `mk` reads its version from the embedded `mygo.json` for that reason. The error kinds, exit statuses, and output streams are a contract that agents script against: the command definitions in `internal/cli` and its `exit-codes` help topic own it, and a new kind needs its exit code and exit-codes line together, with a recovery where one helps. Agents learn the workflow from `resources/skills/maskey/SKILL.md`, which ships in the app; change it with the commands it describes.

## Release builds

The production build must not contain MyGo's automation plugin. For native GUI checks, build a scratch copy with a `//go:build e2e` file that imports `github.com/egoist/mygo/plugins/automation`, with `GOFLAGS=-tags=e2e` and `MYGO_INSPECTOR=1`, without which `mygo build` leaves the plugin out; never add that import to the module. Confirm that `strings` finds no `plugins/automation` in the built executable: `mygo build` strips the symbol table, so `go tool nm` shows no symbol either way. The automation tree of a window covered by other windows is stale, because MyGo builds no frames for it; a `capture` request builds one.

Installed apps update themselves from the latest GitHub Release, accepting only archives signed with the MyGo update key whose public half is `updates.publicKey` in `mygo.json`. Losing that key strands every installed copy, and replacing it needs a release signed with the old one; never read, print, or commit it. Builds without the key, such as Check and local builds, skip the update files.

Pushing a tag `v<version>` runs the Publish workflow, which requires the tag to match the version in `mygo.json` and a `## <version>` section in `CHANGELOG.md` (the update window's release notes), reruns Check, signs the update with the `MYGO_UPDATER_PRIVATE_KEY` secret, attests the archive, and publishes every file through a draft so apps never read a manifest whose archive is missing. The app is signed ad hoc, so user-facing install steps must cover Gatekeeper's quarantine, which Homebrew applies to each version it installs.

After changing an action reference or adding a workflow, regenerate `.github/workflows/actions.lock` with `gh actions-lock`; the Validate actions lock workflow fails on a stale or missing entry. Renovate updates actions together with the lock and holds MyGo upgrades for review, as `.github/renovate.json` explains.

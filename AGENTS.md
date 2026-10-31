# Maskey

Before changing UI code, MyGo usage, the MyGo version, or the build, read `.agents/skills/mygo-maintenance/SKILL.md`. `mygo install-skills` overwrites that directory, so put Maskey's own rules here, not there.

## Checks

```sh
golangci-lint run
go tool mygo vet .
go test -race ./...
```

Use the golangci-lint version that `.github/workflows/check.yml` pins; `.golangci.yml` owns the linters and the file-length limit. Split a file that outgrows the limit by concern within its package rather than raising the limit.

Tests use the in-memory backend in `internal/store/storetest`, fake interpreters, a fake owner authentication, and an activity record in a temporary directory; those that start Apple's interpreter run only the helper's host check, helper code with Core Foundation and Security replaced by Python objects, or a bare socket client, and none makes a Keychain call. They cannot show live Keychain access, sync between Macs, the authentication dialog, or the rendered window.

## The real Keychain

The installed app, `mk`, and `keychain.New` with Apple's interpreter act on the user's iCloud Keychain, which syncs to every Mac on their Apple Account. Run them only with the user's authorization for that session, and then only on synthetic entries in a namespace made for the test, deleted at the end. Never list, read, or migrate entries you did not create; `mk doctor` reads every entry's attributes, so it is not a harmless probe either.

Inside a sandbox that blocks the `com.apple.securityd` Mach services, every direct Keychain call fails with OSStatus -25291 (kind `unavailable`), whatever the code under test does, and `mk` falls back to the Keychain service if it can reach its socket. Run live checks where the Keychain is reachable.

## Persistent identity

`store.Service` and `identifier` in `mygo.json` are the same string: the app's bundle identifier and the service attribute (`kSecAttrService`) of every entry. The account is `<namespace>/<NAME>`, the entry's kind is stored in `kSecAttrDescription` as a `store.ValueKind` string, its notes in `kSecAttrComment`, and its program access in `kSecAttrGeneric`, a JSON object whose `program_access` is `true`. Changing any of these, or the item's `synchronizable` flag, orphans or misreads existing entries on every synced Mac; it needs a migration the user has approved. Entries use `AfterFirstUnlock` so unattended jobs work while the screen is locked; `ThisDeviceOnly` classes cannot sync.

## Values

No path prints, logs, or returns a stored value outside `mk run` (into the child's environment, masked in its output), `mk get` for an entry the owner granted program access, and the window's Reveal, Copy, and Edit form. Program access changes only after the owner authenticates, through the window's switch or through `mk grant` and `mk revoke`, which only the Keychain service carries out because it holds the command line's approval; do not add another path that changes it, a request field that claims an approval, or another get, export, or raw-output path. Values never travel in arguments, temporary files, error messages, or the activity record: `store.Error` carries none, and the helper's stderr stays unread because Python can quote the request there. The record also leaves out notes, command arguments, and the environment, which can carry secrets.

Keep partial-value holdback strict in terminal mode too: a timed flush can expose a value written in pieces, even if it avoids a stalled terminal display.

## Keychain helper and service

`internal/keychain` runs the embedded helper as `/usr/bin/python3 -I -S -c` with only `PATH` in its environment, the request on stdin. The absolute path and the bare environment keep another interpreter, or `DEVELOPER_DIR` redirecting Apple's shim, from hosting it without Apple's `com.apple.python3` identity. The ready line printed after the host check is what separates a confirmed failure (`prerequisite`) from a write that may have happened (`uncertain`); keep both outcomes distinct.

`internal/agent` is the per-user Keychain service for sandboxed callers and for every grant and revoke. Clients fall back to it only on `unavailable`, never on `locked`, `cancelled`, or `uncertain`. Once a write is sent, only the service's answer settles it: a cancelled client withdraws a grant or revoke with a `{"cancel":true}` frame and waits for the answer, and the service cancels only the owner's approval, never a write the owner approved. A connection that ends without an answer is `uncertain`, not a reason to resend. Both peers authenticate as the same signed executable before a request crosses the socket. The service performs only Keychain operations and the owner's authentication for program access; the command and its masking stay in the caller's sandbox.

`Keychain.call` records every Keychain operation; `Store.ChangeAccess` records a refused approval, the one grant or revoke that reaches no Keychain operation; nothing else records. `main`, the service once it authenticates a client, and the window describe their caller with `activity.WithCaller` before any operation, while that program still runs.

## Command line and GUI in one executable

`main` dispatches to the CLI, and to the hidden `--run-monitor` that `mk run` starts on a terminal, before any GUI work. Nothing on a CLI path may call `mygo.App` or `mygo.NewWindow`, because they create the GUI backend; `mk` reads its version from the embedded `mygo.json` for that reason. The error kinds, exit statuses, and output streams are a contract that agents script against: the command definitions in `internal/cli` and its `exit-codes` help topic own it. Give an error a recovery where one helps. Agents learn the workflow from `resources/skills/maskey/SKILL.md`, which ships in the app; change it with the commands it describes.

## Release builds

The production build must not contain MyGo's automation plugin. For native GUI checks, build a scratch copy with a `//go:build e2e` file that imports `github.com/egoist/mygo/plugins/automation`, with `GOFLAGS=-tags=e2e` and `MYGO_INSPECTOR=1`, without which `mygo build` leaves the plugin out; never add that import to the module. Confirm that `strings` finds no `plugins/automation` in the built executable: `mygo build` strips the symbol table, so `go tool nm` shows no symbol either way. The automation tree of a window covered by other windows is stale, because MyGo builds no frames for it; a `capture` request builds one.

Installed apps update themselves from the latest GitHub Release, accepting only archives signed with the MyGo update key whose public half is `updates.publicKey` in `mygo.json`. Losing that key strands every installed copy, and replacing it needs a release signed with the old one; never read, print, or commit it. Builds without the key, such as Check and local builds, skip the update files.

Pushing a tag `v<version>` runs the Publish workflow, which requires the tag to match the version in `mygo.json` and a `## <version>` section in `CHANGELOG.md` (the update window's release notes), reruns Check, signs the update with the `MYGO_UPDATER_PRIVATE_KEY` secret, attests the archive, and publishes every file through a draft so apps never read a manifest whose archive is missing. The app is signed ad hoc, so user-facing install steps must cover Gatekeeper's quarantine, which Homebrew applies to each version it installs.

After changing an action reference or adding a workflow, regenerate `.github/workflows/actions.lock` with `gh actions-lock`; the Validate actions lock workflow fails on a stale or missing entry. Renovate updates actions together with the lock and holds MyGo upgrades for review, as `.github/renovate.json` explains.

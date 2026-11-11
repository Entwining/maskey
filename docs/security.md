# What Maskey protects, and what it does not

With `mk run`, you can give a tool credentials without putting them in prompts or project files. It replaces their exact values in the command's stdout and stderr with a marker. The command still receives ordinary credentials and can do anything with them. Masking does not catch a value the command encodes or transforms, writes to a file, or sends over the network. A terminal program that inserts line breaks or styling within a value can also show it unmasked. Use commands you trust, and wrap the tool that needs the credential rather than a whole coding agent, whose own logs `mk` cannot mask.

For an SSH Key entry, `mk run` reads the private key into its own process and gives the command access to signatures instead of the key. During the run, the command can ask for any signature with that key; Maskey does not approve each signature or restrict it to a host or purpose. The private key is still stored and synchronized in iCloud Keychain, not bound to one device or held in hardware. The window can reveal, copy, or edit it, and granting it Program access lets `mk get` return it like any other value.

Maskey reaches the Keychain through Apple's `/usr/bin/python3`, so any program hosted by that interpreter can reach the same items, without asking you and without a record. The activity record and the 5-minute approval for program access are Maskey's own policy, not protections macOS enforces, and a program running as you can also edit the record unless a sandbox keeps it from writing the record's directory. Entries stay readable after the Mac's first unlock since startup, so unattended jobs keep working while the screen is locked.

## Sandboxed agents

A sandbox that blocks direct Keychain access can use Maskey's per-user Keychain service. Allow this Unix socket in the sandbox's network permissions:

```text
~/Library/Application Support/io.github.entwining.maskey/keychain.sock
```

`mk` uses the Keychain directly where allowed, except for recorded operations when the sandbox cannot write the activity record: those go to the service, which performs and records them outside the sandbox. Do not let a sandbox write `~/Library/Logs/io.github.entwining.maskey`, since a process that can write there can rewrite or delete the record. If the service cannot be reached, `mk` still uses the Keychain directly where it can, and warns when an operation was not recorded.

Both ends check that they are the same signed executable before a request or value is sent. Only Keychain operations and program access approval go through the service; the command and the masking of its output stay in the sandbox. `mk` authenticates with its executable identity, not a stored token.

For SSH Key entries, `mk run` also creates an agent socket in a private directory under `TMPDIR`. The sandbox must allow `mk` to create Unix sockets there and the command to connect to them. This socket lasts only for the run and is separate from the Keychain service's socket above.

# Keeping a Mac node's privacy permissions across updates

For people who run Agents on their own Mac through its resident node and have given that node macOS privacy
permissions (Full Disk Access, Accessibility, Documents and the like). Nothing here is needed to use Persona: without
the setting nothing changes. Issue: #6368 (and #6239 for what an unanswered prompt does).

## Why the permissions go away

macOS privacy permissions (TCC) remember a program by its designated requirement (DR). Go's linker signs darwin builds
ad hoc, with the identifier `a.out` and no certificate:

```console
$ codesign -dv ~/.persona/bin/personad
Identifier=a.out
CodeDirectory v=20400 size=… flags=0x20002(adhoc,linker-signed) …
Signature=adhoc
TeamIdentifier=not set
$ codesign -d -r- ~/.persona/bin/personad
# designated => cdhash H"…"
```

An ad-hoc DR is the build's cdhash, so every node update (Redeploy, the hub's ssh deploy, tailcat,
`scripts/deploy/deploy.sh`, `install.sh`) installs a program TCC has never seen: the permissions stop applying and macOS
asks again, or a prompt waits on a screen nobody is looking at. Signing ad hoc again under a fixed identifier
(`codesign -f -s - -i com.nwverse.personad`) changes the name only; the DR stays `cdhash H"…"`.

A certificate's signature has a DR made of the identifier and the certificate:

```text
designated => identifier "com.nwverse.personad" and anchor apple generic and certificate leaf[subject.CN] = "Apple Development: <name> (<id>)" and certificate 1[field.1.2.840.113635.100.6.2.1] /* exists */
```

Every build signed with the same certificate under the same identifier meets it, so permissions granted once keep
applying through updates.

## Setting it up

1. **A code signing certificate in the Mac's login keychain.** An Apple Development certificate works: Xcode › Settings ›
   Accounts › Manage Certificates makes one for any Apple ID. List what this Mac can sign with:

   ```sh
   security find-identity -v -p codesigning
   #   1) <40 hex digits> "Apple Development: me@example.com (ABCDE12345)"
   ```

2. **Let codesign use the key once, in Terminal**, so the keychain never holds the node up asking. Sign any copy and
   answer the dialog with the login password and **Always Allow**:

   ```sh
   cp /usr/bin/true /tmp/sign-check && codesign -f -s "<certificate name>" /tmp/sign-check
   ```

3. **Write `~/.persona/node-signing.json`** (the node's data directory; `PERSONA_HOME` moves it):

   ```json
   {"identity": "Apple Development: me@example.com (ABCDE12345)"}
   ```

   `identity` is the name exactly as `security find-identity` prints it between the quotes, or its 40-digit SHA-1.
   `identifier` is optional: `com.nwverse.personad`, or `com.nwverse.persona-node` for a node that runs `persona-node`.

4. **Restart the node**: `launchctl kickstart -k gui/$(id -u)/com.persona.node`. At start it signs its program and runs
   the signed file. Check:

   ```sh
   codesign -dv ~/.persona/bin/personad      # Identifier=com.nwverse.personad, TeamIdentifier=<your team>, no Signature=adhoc
   codesign -d -r- ~/.persona/bin/personad   # designated => identifier "com.nwverse.personad" and … certificate leaf …
   grep 'node signing' ~/.persona/node-attach.log
   ```

5. **Grant the permissions once more.** The first signed build is a new program to macOS. In System Settings › Privacy &
   Security, for Full Disk Access and every other list the node is in, turn `personad` off and on again, or remove it and
   add `~/.persona/bin/personad` again. From then on updates keep the permissions.

**Turning it off**: delete the file and restart the node. The program stays signed until the next update replaces it;
after that builds run with the signature they come with, and permissions last one build again.

## When the node signs

Only the resident node launchd runs (`com.persona.node`, `node --attach`) signs, in the login session, where the login
keychain is open: from an ssh session codesign fails with `errSecInternalComponent`.

- **An update over the node RPC** (Redeploy of a computer connected through Persona Link): once the upload's size,
  sha256, build info and self-check pass, the node signs a copy of it, runs the signed copy's `node-update-check`, and
  swaps in that copy. The transaction records the signed file's sha256, so confirmation and rollback
  ([node-update.md](node-update.md) ("Restart and rollback")) compare the installed program with what was actually
  installed. The upload stays as it came until the swap, so a retried commit checks it against the hub's sha256 again.
- **Any other path** (the hub's ssh deploy script, tailcat, `scripts/deploy/deploy.sh`, `install.sh`, a copy by hand):
  the resident node signs its program at start and before a reload when the file does not meet the setting. A signed,
  checked copy replaces the file only while the file is still the build it was copied from, and the node then runs the
  signed file (an exec: same process). An update awaiting confirmation is left alone.
- **Nothing else compares the program's bytes.** The hub, its deploy scripts and Redeploy compare versions
  (`personad version`, `node.info`), which signing does not change; persona-link copies a program only where there is
  none.
- **A failure never stops the node.** It is logged (`node signing: …` in `node-attach.log`). An update then installs
  signed ad hoc under the identifier (a new build has a new cdhash anyway), or as it came when even that fails. At start
  the program keeps the signature it has, which may still match the permissions given to it. Usual causes: the
  certificate's name is not exact, the keychain is locked or asks (codesign gets 15 seconds), the certificate expired.

The node of the hub's own Mac runs under the hub's or the manager's agent, and macOS holds it to that agent's program
([server.md](../server/server.md)); this setting does not sign those programs, nor the Persona Link app.

**The live-screen video helper (Persona Screen) uses the same setting.** Its Screen Recording grant is its own, so
signing the node alone wouldn't keep it. With this setting the node runs its own copy, `~/.persona/bin/Persona
Screen.app`, signed with the same certificate under the helper's bundle id (`<link id>.screen`). It copies and signs
again only when Persona Link installs a different helper, and checks the copy meets the requirement before it replaces
the old one. It never re-signs the helper inside Persona Link.app, since that would break the app's own seal. When
signing fails it runs the installed helper, as the node itself does. The first signed copy is a new program to macOS:
allow **Persona Screen** under Screen & System Audio Recording once more. Without the setting the installed helper
runs; Persona Link builds it the same way each time, so its grant lasts until the helper itself changes
([computer-use.md](../extensions/computer-use.md) ("Live screen video (#3954)")).

## Builds

`scripts/deploy/deploy.sh` and `scripts/release/publish.sh`, run on a Mac, sign their darwin builds ad hoc again as
`com.nwverse.<program>` instead of `a.out`. That gives the builds a readable name and nothing more: their DR is still the
cdhash. Builds made elsewhere (the Docker image, Linux release workers) keep `a.out`.

## Official releases: options for the maintainers

This setting is per computer and needs the owner's own certificate. For the builds everyone downloads, the choice is the
maintainers':

1. **Developer ID Application certificate and notarization** for the darwin `personad`, `persona-node` and
   `persona-link`: one fixed DR for everyone, no setup on any Mac, and Gatekeeper accepts downloaded builds. It takes a
   paid Apple Developer membership, a Mac signing step in the release with access to the key, and a `notarytool`
   submission per release. A bare executable cannot have its ticket stapled, so its first launch checks with Apple
   online.
2. **Keep releases ad hoc** and leave stable signatures to this per-computer setting, as now.

Either way, a change of signing identity is a new DR: everyone grants the permissions once more after it.

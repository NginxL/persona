# Desktop app and Persona Link

The desktop experience for macOS and Windows is an Electron shell around the hub-hosted web app. It is not a second chat interface, a second design system or a replacement for the computer's node service.

## Two independent installations

**Persona desktop** is for everyone who wants to chat and use Agents on any computer. It needs no local node and no Persona Link. It supplies the existing web sign-in and screens, native notifications, the existing Needs-you badge, desktop presence, single-instance behavior, window-state restoration, deep links, a tray with close-to-background behavior, a visible launch-at-login setting and public-release update wiring.

**Persona Link** is only for a computer whose Agents the hub should reach. Its CLI and service are independently installable and useful without the desktop app, including on Linux and headless computers. The connector owns the hub connection, node-service management and the Persona Screen recording helper. Do not delete these capabilities when removing an old desktop UI.

When an installed Link is present, the desktop's local setup page can show its redacted status and offer fixed operations for pairing, reconnect and node-service installation. Link remains a separate install; the desktop app never silently installs a background service or bundles one as a prerequisite. When it is absent, those controls are disabled and the page offers the separate installation guide.

## Existing web behavior

- The desktop loads only the configured hub origin (including its reverse-proxy prefix). Other HTTP/HTTPS links open in the system browser. Authentication, service-worker permission actions, notification text/tags/deduplication/withdrawal, quiet hours and the Needs-you count stay in the existing web code.
- Native notification clicks return to the existing SPA route. Native display failure keeps the browser fallback. Signing out clears the previous account's native notices.
- `register_desktop` uses the existing authenticated web socket, never a token in the Electron main process. An active, unlocked, recently used desktop with notifications may register. Locking, idling or disabling notifications withdraws it. New hubs use `unregister_desktop`; older hubs use socket-close withdrawal and reconnect only when active and notifying again. Resume alone does not mean unlocked; only the actual unlock event restores presence.
- Standard platform TLS validation is used. There is no broad certificate bypass and no unverified saved-pin shortcut.

## Process and privilege boundaries

`desktop/` uses TypeScript and Electron. The hub renderer has `contextIsolation: true`, `nodeIntegration: false`, sandboxing, web security, no webviews, no Node workers/subframes and no production DevTools. The privileged local setup window has a different preload from the hub window.

Preloads expose fixed operations, not arbitrary IPC, WebSocket messages, paths, tokens or process execution. Desktop IPC checks the owning window, current main frame and expected origin. Link commands use only `~/.persona/bin/persona-link` (`.exe` on Windows), fixed argv, no shell and pairing input on stdin. Status returns only the state and names needed by the UI, not `link.json`, internal addresses, PIDs, logs or node errors. Per-window handlers are removed when that window closes.

### One bridge type, additive compatibility

`spec/desktop-bridge.d.ts` is the single pure-type `DesktopBridge` contract shared by the web renderer and desktop preload. `web/` and `desktop/` remain separate build roots: both use `import type` and neither bundles a runtime module from that file. The hub-window preload annotates its complete implementation with `satisfies DesktopBridge`; web mocks use the same type. Runtime route validation and the local setup-window's separate privileged contract are not part of this type-only file.

**Compatibility rule:** the desktop application may update more slowly than the hub-hosted web it loads. Version 1 therefore only adds capabilities. The web checks for an additive method or field before using it and keeps its browser fallback when an older desktop does not provide it. A removed or incompatible method, parameter, return value or meaning requires incrementing `version` and an explicit migration, never silently changing the version-1 contract.

Closing the main window hides it to the tray while the signed-in renderer stays alive. The tray opens the current main window, not a destroyed setup window; explicit Quit exits. Only the desktop app itself is controlled by launch at login, not the Link service. macOS may still require approval in System Settings, which the setting must report rather than claim success.

## Packaging and verification

`electron-builder.yml` defines universal macOS DMG/ZIP and Windows NSIS artifacts. `electron-builder.unsigned.yml` uses a temporary development app ID and explicitly disables certificate discovery/signing and notarization. It is for development only. Unsigned artifacts do not prove signed distribution, notarization or installation.

CI has a Linux job (policy tests, the web client's build and the headless acceptance smoke), macOS universal packaging and Windows NSIS jobs, each with the acceptance smoke on its own runner and its screenshots uploaded (`persona-desktop-<os>-smoke`). Windows is built on Windows, not via a failed Wine cross-build. The package steps run whether or not the smoke passed, so each reports its own verdict. The Linux job builds the web client once (`scripts/ci/desktop-web.sh`) and hands the bundle to the other two jobs. The desktop lane needs that bundle, not the web lane's verdict: when the only thing that failed in the web build is its first-screen size budget, checked after every file is written, the script warns and the smoke runs on the complete bundle; any other build failure fails the job. The development workflow builds unsigned packages only and never receives signing credentials; signing and notarization run only in the release workflow. Signed artifacts require explicitly supplied credentials and an authorized run; no certificate is purchased, inferred from App Store signing or sought from a private key store. The secrets and what each must contain are listed under "What the owner must provide" below.

### The acceptance smoke

`scripts/ci/desktop-smoke.sh` (`node desktop/scripts/smoke-with-hub.mjs [output directory]`) runs a real Electron process against the repository's synthetic hub. A small local gateway (`desktop/scripts/smoke-gateway.mjs`) serves the built web client the way the hub does: its files, `index.html` for page routes, `/api/` and the WebSocket forwarded to the hub. Every service is on a port the script owns and is stopped by it. The hub refuses every request without a credential; the app signs in with a temporary pairing code made for the run, through the same `persona://pair` path a person's pairing link takes. It needs `web/dist` (built when missing, which needs Go) and ends with one verdict line; `result.json` holds each scenario's steps, and each scenario's directory its screenshots, per-step details and Electron log.

Two scenarios run, each in its own profile:

- **Origin root**, against a hub that lists `desktop_unregister`: the hub's sign-in page, sign-in, then the main chat (light and dark), the Agents list, an Agent session with a waiting card, Inbox and To-dos, each captured once the hub's data is in the page's DOM. With the window closed to the tray, a new Needs-you item from the hub must produce a notification request with the session's title, tag and route, the badge the web's own count, and a click must show the window on that session. Locking withdraws the desktop registration with `unregister_desktop`; unlocking registers again.
- **Reverse-proxy prefix**, against an older hub: the gateway answers only below the prefix and rewrites `<base>` as the hub does, and any request of the page outside the prefix fails the run. Sign-in, the chat and the Agents list load; an in-hub link meant for a new window stays in the window; an off-hub link, an off-hub navigation and the same origin outside the prefix are refused and handed to the system browser. Locking withdraws the registration by closing the socket.

The driver, `desktop/scripts/e2e-main.mjs`, is Electron's entry point for the run and then loads the unmodified production main module. It is not part of `dist/`, so it is never packaged; it refuses to run packaged or without its switch, gives the hub renderer nothing and adds no IPC channel. It only watches or replaces the outer edge of the main process:

| Observed or replaced | What the smoke therefore proves | What it does not |
| --- | --- | --- |
| `Notification.prototype.show` / `close` record the call; a click is the notification's own `click` event | The web's request crosses the production preload and IPC, passes route validation and reaches the system call with the right text; the click handler shows the window and routes | That the OS notification center displays, groups or delivers a click, and its permission prompt |
| `Notification.isSupported` and the idle time answer "supported" and "just used" | Presence logic from those inputs to `register_desktop` / withdrawal on the authenticated socket | Real idle detection and a runner's own notification service |
| `lock-screen` / `unlock-screen` are emitted on the power monitor | The shell's reaction to the events | That the OS emits them when a person locks the computer |
| `shell.openExternal` records the address | The decision for each link | A browser opening |
| `desktop:badge` is read beside the real handler; on macOS the Dock's count is read back | The count sent equals the web's | The Windows taskbar overlay and the Dock's drawing |

On a Linux machine without a display server the smoke uses Chromium's headless platform and leaves out the tray's menu, which GTK cannot build there; Linux is not a shipping platform. The smoke runs the development build, not an installed package: it does not prove installation, signing, notarization, the tray's own interactions, launch at login or an update from a published release. Generated output is cleaned before compilation so deleted tests cannot linger, and a scenario's directory is emptied before it runs. Linux/macOS development evidence does not substitute for Windows acceptance or a published-release update test.

`npm --prefix desktop run smoke` remains the quick check of the first window against a hub already running at `PERSONA_HUB_URL`.

## Updates

**The default, in one sentence:** Persona checks for a new version when it opens and every four hours, downloads it in the background, and installs it only when you quit or choose **Restart to update**. Nothing restarts the app while you are working, and there is no setting to configure.

### What people see

The update state lives in the tray menu, between **Open Persona** and **Quit Persona**. There is no web UI for it and no bridge method.

| State | Tray entry | Enabled |
| --- | --- | --- |
| Nothing to report | Check for updates | yes |
| A check is running | Checking for updates… | no (status line) |
| A new version is downloading | Downloading version 1.2.0… | no (status line) |
| Downloaded, waiting for a restart | Restart to update to 1.2.0 | yes |
| After a check you asked for found nothing | "Persona is up to date", then Check for updates | status line, then yes |
| After a check you asked for failed | "Couldn't check for updates. Persona will try again later.", then Check for updates | status line, then yes |

- **Check for updates** changes to "Checking for updates…" at once, before any network work. Asking again while a check runs does not start a second one.
- Only a check the person asked for leaves a result line. A scheduled check that finds nothing, or fails, changes nothing visible; the next scheduled check retries. A failure never blocks or covers the web UI.
- An update the platform has accepted stays offered until the app restarts. On macOS that is Squirrel.Mac's own acceptance, not electron-updater's earlier file-downloaded event; a refusal returns to the retry schedule instead of offering a restart that cannot work. Quitting installs it silently; **Restart to update** installs it and reopens Persona. No further checks run in that session.
- The schedule is fixed (launch, then every four hours, no random delay), so it is the same on every run and unit-tested with a fake clock (`desktop/test/update.test.ts`).
- Development runs and Linux have no update feed, so the tray shows no update entries there.
- The feed is the one written into the package at build time (`app-update.yml`: this repository's GitHub Releases). No environment variable, setting or hub page can point a packaged app at another feed; `PERSONA_DESKTOP_DISABLE_UPDATE=1` only turns checks off. A test pins that the electron-updater calls which would override the feed are never used.
- Tray labels and update strings come from `desktop/src/locales` in English, Simplified Chinese and Japanese.

### How a release publishes desktop packages

A stable `v<major>.<minor>.<patch>` tag runs `.github/workflows/release.yml` (`docs/operations/release.md`, "Desktop packages on version tags"). Behind the same administrator-only gate as the hub image, it:

1. decides per platform whether every signing secret below is configured (`scripts/ci/desktop-release-gate.sh`; only whether a secret exists reaches the script, never its value);
2. builds on native runners with `scripts/ci/desktop-package.sh`: the universal macOS DMG and ZIP, signed and notarized, and the Windows NSIS installer, signed. The package version is the tag without its `v`; the app ID comes from the `PERSONA_DESKTOP_APP_ID` secret and is never written in the repository;
3. verifies the result (`codesign` and Gatekeeper's `spctl` on macOS, `Get-AuthenticodeSignature` on Windows);
4. attaches the packages and their block maps to the GitHub Release the workflow already created, and only then the update metadata (`latest-mac.yml`, `latest.yml`), so an installed app never reads metadata naming a package that is not there yet.

**A release never ships an unsigned package as if it were final.** When a platform's secrets are missing, that platform is skipped, the run's summary names the missing secrets, and the hub image and GitHub Release are published as before. In release mode the package script refuses to build without a certificate or with the placeholder app ID instead of falling back to the unsigned development build. Prerelease tags (`v1.3.0-rc1`) publish no desktop packages: installed apps follow the latest stable Release only.

Between the moment the Release is created and the moment its desktop metadata is attached, the newest Release has no update metadata. A check in that window fails quietly and the next scheduled one succeeds.

### What the owner must provide

No certificate exists yet, so no release has published desktop packages. Before adding credentials, the owner must create the `persona-desktop-release` GitHub Environment with required reviewer approval restricted to the administrator, and configure these as **Environment secrets**, not repository secrets. The environment restricts their use to the release jobs on stable version tags. The development Desktop workflow never receives them. None is ever committed, and nobody should look for them in a key store.

| Secret | What it is |
| --- | --- |
| `PERSONA_DESKTOP_APP_ID` | The app's registered identifier (reverse-DNS, the macOS bundle ID). It must stay the same for every release: macOS ties the signature and Windows ties the installed copy to it. |
| `MAC_CSC_LINK` | Base64 of a `.p12` export holding a **Developer ID Application** certificate with its private key. Apple Development, Apple Distribution (App Store) and Developer ID Installer certificates do not work. |
| `MAC_CSC_KEY_PASSWORD` | The password chosen when exporting that `.p12`. |
| `APPLE_API_KEY` | The text of an App Store Connect API key file (`AuthKey_<key id>.p8`), used for notarization. The build writes it to a private temporary file and removes it afterwards. |
| `APPLE_API_KEY_ID` | That key's ID. |
| `APPLE_API_ISSUER` | The issuer ID shown on the App Store Connect API keys page. |
| `WIN_CSC_LINK` | Base64 of a `.pfx` holding an Authenticode code-signing certificate (OV or EV) with its private key. |
| `WIN_CSC_KEY_PASSWORD` | The password of that `.pfx`. |

Two things to know before buying anything:

- **macOS updates need the Developer ID signature, not just the download.** The macOS updater (Squirrel.Mac) installs a new version only into a Developer ID signed app, and only when the new version is signed by the same team. An unsigned or ad-hoc build finds and downloads the update and then cannot install it; the update test below records exactly that. electron-updater's own "downloaded" event fires before Squirrel.Mac has judged the file, so it is not evidence of an installable update on macOS.
- **Windows certificates issued since mid-2023 usually cannot be exported.** Certificate authorities now keep the private key on a hardware token or in a cloud HSM, so there may be no `.pfx` to put in `WIN_CSC_LINK`. Signing with such a certificate needs a cloud-signing step in place of the `.pfx` (for example the authority's signing service). Which service to use is the owner's decision and is not implemented. An unsigned Windows package does update (the updater checks the download's SHA-512 against the metadata), but Windows SmartScreen warns on install and the updater cannot check the publisher.

### The update test

`node desktop/scripts/update-test.mjs` performs a real update with no certificate and no published release. It builds a test-only package (`desktop/electron-builder.update-test.yml`) twice, as version 0.9.0 and 0.9.1, with a local feed address written into the package the same way a release's feed is; serves the new build's directory on a free loopback port; runs the **packaged** old version with a saved hub address and an isolated profile; and checks what it does. The test-only entry (`desktop/update-test/main.mjs`) records the updater's events and quits once the download is ready, then loads the real main process unchanged. It is listed only in the test configuration, and the test build has its own name so its install directory, profile and update cache never touch a real install.

| System | What is checked |
| --- | --- |
| Windows (hosted runner) | Silent per-user NSIS install of the old version into a temporary directory; the app finds the new version, downloads it, and the file's SHA-512 equals the metadata's; quitting installs it; the reopened app reports the new version and finds nothing newer; then uninstall. |
| macOS (hosted runner) | The old version runs from its ZIP with an ad-hoc signature (no certificate). It finds the new version and downloads the ZIP, whose SHA-512 equals the metadata's. The test then follows Squirrel.Mac's own verdict rather than the download event: on the hosted runner Squirrel.Mac refuses the file ("did not pass validation: code failed to satisfy specified code requirement(s)"), and the test reports that refusal instead of an install. Had it accepted, the test would require the install and the reopened version as on Windows. |
| Linux (AppImage) | The same as Windows through an AppImage, under Xvfb. The product ships no Linux package; this exercises the shared feed, differential download and checksum code where no macOS or Windows computer is available. |

The Desktop workflow runs it on all three systems (`update` job in `.github/workflows/desktop-test.yml`) and uploads `result.json` and the app's log.

Not proven by this test: a signed build updating from a published GitHub Release, the macOS install, notarization, and the tray entries as drawn on screen (their text and order are unit-tested; no screenshot of a native menu is taken).

## Migration boundaries

The retired `PersonaMac` target and native-Mac source directory are removed separately, with iOS, watchOS, visionOS and shared package checks. The old Mac menu bar UI and Windows `linktray` are removed too: independent Link installation, pairing/reconnect/status/service controls and Persona Screen packaging now use the CLI and fixed desktop setup bridge. The CLI/service works without the desktop app; Mac uses launchd `com.persona.link`, Windows retains its scheduled task.

Remaining platform acceptance, signed/notarized artifacts, Windows installation and a published auto-update belong to the migration issue (#6770), not to a claim that the shell or independent Link packaging completes the migration.

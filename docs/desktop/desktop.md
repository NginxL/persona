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

CI has a Linux job (policy tests, the web client's build and the headless acceptance smoke), macOS universal packaging and Windows NSIS jobs, each with the acceptance smoke on its own runner and its screenshots uploaded (`persona-desktop-<os>-smoke`). Windows is built on Windows, not via a failed Wine cross-build. The package steps run whether or not the smoke passed, so each reports its own verdict. The Linux job builds the web client once (`scripts/ci/desktop-web.sh`) and hands the bundle to the other two jobs. The desktop lane needs that bundle, not the web lane's verdict: when the only thing that failed in the web build is its first-screen size budget, checked after every file is written, the script warns and the smoke runs on the complete bundle; any other build failure fails the job. Signed artifacts require explicitly supplied repository credentials and an authorized run; no certificate is purchased, inferred from App Store signing or sought from a private key store.

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

## Migration boundaries

The retired `PersonaMac` target and native-Mac source directory are removed separately, with iOS, watchOS, visionOS and shared package checks. The old Mac menu bar UI and Windows `linktray` are removed too: independent Link installation, pairing/reconnect/status/service controls and Persona Screen packaging now use the CLI and fixed desktop setup bridge. The CLI/service works without the desktop app; Mac uses launchd `com.persona.link`, Windows retains its scheduled task.

Remaining platform acceptance, signed/notarized artifacts, Windows installation and a published auto-update belong to the migration issue (#6770), not to a claim that the shell or independent Link packaging completes the migration.

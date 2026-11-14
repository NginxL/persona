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

CI has Linux policy tests, macOS universal packaging and Windows NSIS jobs, with native-runner Electron smoke and screenshots. Windows is built on Windows, not via a failed Wine cross-build. Signed artifacts require explicitly supplied repository credentials and an authorized run; no certificate is purchased, inferred from App Store signing or sought from a private key store.

The real smoke starts an isolated Vite server and the repository's synthetic hub on owned ports, launches Electron with an isolated profile, waits for actual login/setup DOM and the entrance transition, captures the page and exits with a one-line verdict. Generated output is cleaned before compilation so deleted tests cannot linger. Linux/macOS development evidence does not substitute for Windows acceptance or a published-release update test.

## Migration boundaries

The retired `PersonaMac` target and native-Mac source directory are removed separately, with iOS, watchOS, visionOS and shared package checks. The old Mac menu bar UI and Windows `linktray` are removed too: independent Link installation, pairing/reconnect/status/service controls and Persona Screen packaging now use the CLI and fixed desktop setup bridge. The CLI/service works without the desktop app; Mac uses launchd `com.persona.link`, Windows retains its scheduled task.

Remaining platform acceptance, signed/notarized artifacts, Windows installation and a published auto-update belong to the migration issue (#6770), not to a claim that the shell or independent Link packaging completes the migration.

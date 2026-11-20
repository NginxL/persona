# Meta smart glasses

Let Persona use Meta smart glasses (Ray-Ban Meta Gen 1 / Gen 2, Meta Ray-Ban Display, Oakley Meta HSTN / Vanguard):
use the glasses' microphone as a recording input, and take a camera photo as chat context. No second recording product and no interface on the glasses. Issue #6.

## In brief

- **Glasses microphone**: phone recording on the Recordings page (`RecordingCaptureControls`, called "Recording context" before #3911) gains a "Microphone" option (phone microphone / glasses or Bluetooth headset). It uses standard Bluetooth HFP, requires no Meta SDK, and does not affect anything disabled by default.
- **Glasses photo**: enable Meta's Wearables Device Access Toolkit (DAT) through configuration. "Glasses Photo" in the input bar's attachment menu lets you take a photo while viewing a live preview and attach it to a message. Not compiled into the App by default.

## Meta's third-party integration options (verified 2026-11)

| Capability | Access method | Status |
| --- | --- | --- |
| Microphone (for the wearer's speech) | Standard Bluetooth HFP: `AVAudioSession` with `.playAndRecord` + `.allowBluetoothHFP`, select the `.bluetoothHFP` input | System capability, like a regular Bluetooth headset; 16 kHz or 8 kHz mono, beamformed to emphasize only the wearer's own voice |
| Camera video stream | DAT `MWDATCamera`: `DeviceSession` → `addCamera(config:)` → `camera.stream`, 360×640 / 504×896 / 720×1280, 2–30 fps | Stable API |
| Photo capture | With the stream running, `stream.capturePhoto(format: .jpeg)` returns results through `photoDataPublisher`; higher-resolution standalone capture uses `camera.photo` | The former is stable; the latter experimental |
| Audio alongside the video stream (not beamformed) | `StreamConfiguration.audioCodec` + `audioFramePublisher` | Experimental, cannot be released, and requires the camera stream to stay on |
| Glasses buttons, IMU, on-glasses speech transcription, "Hey Meta" invocation | `MWDATInputs`, `MWDATMotion`, `MWDATSpeech`, `VoiceInvocationsStream` | All experimental, with separate approval from the developer center |

Requirements and limitations:

- The SDK is `https://github.com/facebook/meta-wearables-dat-ios` (SPM). From 1.0 onward it requires **Xcode 26.4+ / Swift 6.3+**, iOS 17.2+.
- Pair the glasses in the **Meta AI app** first. During development, enable Developer Mode in Meta AI; no Meta application needs to be created (`MetaAppID` of 0 skips validation).
  For distribution to others, create an app in Wearables Developer Center, obtain an App ID and Client Token, and deliver it to testers through its release channel.
- The App first "registers" in Meta AI (switching to Meta AI and then back to Persona). Camera permission is also granted in Meta AI; both steps switch apps.
- The SDK uses ExternalAccessory. Meta's documentation warns that App Store submission may be blocked by MFi review; external TestFlight Beta review carries the same risk.
- The documentation does not specify background behavior; the official sample ends the preview session when the App goes into the background.

References: [iOS integration](https://wearables.developer.meta.com/docs/develop/dat/build-integration-ios/),
[microphones and speakers](https://wearables.developer.meta.com/docs/develop/dat/microphones-and-speakers/),
[FAQ](https://developers.meta.com/wearables/faq/), [SDK repository and CHANGELOG](https://github.com/facebook/meta-wearables-dat-ios).

## Why not build on rolling recording

Rolling recording (#5, PR #2) continuously records on the Watch, retains only 6 hours, and adds heart-rate bookmarks. The glasses are not a good audio source for it:

- Third parties can access the glasses' microphone only through HFP. Beamforming makes other people nearby very quiet, so it is worse than the phone or Watch for meetings and ambient recording.
  Keeping HFP on continuously drains the glasses' battery (which already lasts only a few hours), and Bluetooth output drops from A2DP to call quality, degrading music playback.
- Non-beamformed audio is available only in DAT's camera stream. It is experimental (cannot be released), requires the camera stream to stay on, and consumes power / generates heat.
- Rolling recording's value comes from a Watch that is always worn and provides heart-rate data. The glasses have no health data, so the bookmarking half is not useful.
- PR #2 has not merged yet. Building on top of it would require its branch and make the two efforts block each other.

This round therefore starts from master: recording changes only "which microphone to use," with that choice in `RecordingCapture`. After #2 merges, if the phone also needs rolling recording,
the glasses microphone is simply the same input option, with no separate implementation needed.

## 1. Glasses microphone

- `RecordingInput` (`apple/Shared/Recording/RecordingInput.swift`): `.phone` / `.bluetooth`, with the selection stored in `recording.input`.
- The recording page shows the "Microphone" option only when the current audio route includes a Bluetooth device (HFP / A2DP / LE). With no Bluetooth connection, the UI is unchanged.
- With glasses selected, the session uses `.playAndRecord` + `.allowBluetoothHFP`. After activation it waits up to 2 seconds for an HFP input to appear (Meta's documentation says to wait for the route to stabilize),
  then selects it as the preferred input. If none appears, it falls back to the phone microphone and shows a notice, rather than failing the recording. The microphone currently in use is named during recording.
- Writing is unchanged: the segment engine converts the input format to 16 kHz mono before encoding, accepting both 8 / 16 kHz HFP. If the glasses are removed or folded mid-recording, route changes use the existing
  `AVAudioEngineConfigurationChange` handling (restart the engine; pause if that fails).
- No Meta SDK is involved. Any Bluetooth headset can work this way (AirPods use HFP, too).

## 2. Glasses photo (optional build)

### How to enable it

1. Set `app.meta_wearables.enabled = true` (`docs/operations/config.md`). For distribution to others, also provide `app_id` and `client_token`.
2. Use Xcode 26.4 or newer (`xcode` in the configuration). `persona_config.py env` exports `PERSONA_META_WEARABLES=YES`,
   so `xcodegen` merges `apple/meta-wearables.yml` into the project: SPM packages `MWDATCore`, `MWDATCamera`, and a post-build script that adds the Info.plist keys required by the SDK
   (`MWDAT`, `UISupportedExternalAccessoryProtocols`, Bluetooth / accessory background modes, Bluetooth and local-network usage descriptions, `NSBonjourServices`) to the built plist.
   Callbacks use the App's already registered `persona://`.
3. When disabled: neither the package nor these keys is present, `MetaGlasses.isBuiltIn` is false, "Glasses Photo" is absent from the menu, and Xcode 26.0 can still build it.
4. The post-build script merges these keys into the built Info.plist on every build, appending array entries only when absent. The tracked `apple/Persona/Resources/Info.plist` stays unchanged. `Package.resolved` carries the pinned Meta SDK even when the overlay is off, and `release.sh` disables automatic package-resolution rewrites, so enabling the overlay does not dirty the release worktree.

### Code

- `apple/Persona/Glasses/MetaGlasses.swift`: the real implementation is compiled only under `#if canImport(MWDATCore) && canImport(MWDATCamera)`; otherwise it is a stub.
  At launch, call `Wearables.configure()`; in `onOpenURL`, hand it the URL first (it recognizes only callbacks containing `metaWearablesAction`). Sessions use `AutoDeviceSelector`.
  Preview is `.raw` / 504×896 / 24 fps; photo capture uses the stable `stream.capturePhoto(format: .jpeg)`, falling back to the current preview frame if unavailable. Closing the page stops the session.
- `apple/Persona/Glasses/GlassesCaptureSheet.swift`: not registered → "Connect Meta Glasses" (switches to Meta AI); no camera permission → "Allow" (also explains the app switch first);
  ready → live preview + "Take Photo" → "Use This Photo" / "Retake". Using a photo adds it to the input bar just like a camera photo; it reaches the Agent only when Send is tapped.
- The entry point is added only to the chat page's input bar (all three input bars). The assistant main-chat entry point will be added later.

## Out of scope

- No display on the glasses (Display glasses' display, Web Apps), button integration, or "Hey Meta": all are experimental and cannot be released.
- No recording glasses video or keeping the camera stream on for long periods: it consumes power / generates heat, and no user has requested it.
- Raw audio and photos are not uploaded automatically. As in `docs/apple/recording.md`, they reach the Agent only when the user taps Send.

## Verification required on Mac and physical devices

- With the option disabled, Xcode 26.0 builds iOS, Mac, visionOS, and Watch (visionOS does not use the Bluetooth microphone). With it enabled, Xcode 26.4+ builds successfully, and the post-build script in `meta-wearables.yml`
  appends to `UIBackgroundModes` in the built Info.plist rather than overwriting it (inspect that built plist).
- Unit tests: `RecordingInputTests`; dark / light screenshots of the recording page, both with and without Bluetooth connected.
- Physical device: record for 5 minutes using the glasses microphone; check transcription quality, other people's speech volume, and removal of the glasses during recording. Test the full registration → camera permission → photo capture → send to Agent flow,
  and the preview after backgrounding and returning to the App.
- Check whether an enabled build passes Beta review for external TestFlight (ExternalAccessory). If it does not, distribute only to the internal group or keep it as a local build.

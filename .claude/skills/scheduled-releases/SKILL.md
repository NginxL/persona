---
name: scheduled-releases
description: Publish Persona stable releases at noon local time and independent immutable beta prereleases every two hours, with signed platform verification and separate update channels. Use when asked to run, schedule, resume or verify these daily/beta releases.
---

# Scheduled stable and beta releases

Compose the existing release tools; do not create a second release pipeline. This skill is for the release operator. It does **not** install a scheduler or publish anything by itself.

## Schedule and scope

- **Stable:** once per day at **12:00 in the operator's configured local time zone**. Do not silently interpret that as UTC or as another machine's zone.
- **Beta:** once every **2 hours**, never every 2 minutes, in the same explicitly configured zone.
- On an actual invocation, first inspect the latest `origin/main`, today's unfinished stable work and existing beta issues/PRs. Complete or collaborate on unfinished stable work before starting a new beta. Never rebuild a platform that already completed for the same immutable commit.
- Installing or reading this skill authorizes no publication, deployment, store submission or scheduler installation. Those require the appropriate explicit operator action and the machine's existing private configuration.
- The supported unattended default-branch worker is `scripts/operations/autorelease.py` with `scripts/operations/persona-autorelease.service` and `scripts/operations/persona-autorelease.timer`. Its target/path selection is not a beta scheduler. Do not run duplicate schedules or change an existing schedule just because this skill exists.

Read `docs/operations/release.md`, `scripts/operations/tag_release_prompt.md`, `docs/operations/hub-update.md`, `docs/operations/collaboration.md`, and the repository's super rules before acting. Follow issue → claim → branch → local verification → PR → merge for any repair or tooling change.

## Stable release at noon

1. Find duplicate and unfinished stable release issues, and verify the latest stable Release and tag. If an older attempt is incomplete, keep its tag immutable and repair in a new version, not by replacing its source or assets.
2. Compare the latest stable tag with current `origin/main`, including substantive feature commits that do not use the `feat` prefix:
   - New feature since the previous stable tag → increment the minor version and reset patch to zero.
   - Only fixes → increment the patch version.
   - No changes needing publication → report that the run is skipped.
3. Claim the issue with the selected exact commit, plan and ETA. Create a clean detached worktree and a new immutable tag for that exact commit. Never move an existing tag or publish a different branch/commit under it.
4. Use the fixed commit's existing container workflow to create the stable GitHub Release. Independently verify the container and build formally signed products through the existing platform tools:
   - CLI/Web: `scripts/release/publish.sh` from the pinned worktree with a release channel manifest and `NO_DEPLOY=1` unless deployment was explicitly requested. Do not bypass its ancestry guard or deploy the website merely to obtain artifacts.
   - Android: `scripts/release/android_tag_release.py` through `scripts/operations/tag_release.py` and the machine's mode-0600 private platform configuration. Verify tests, R8, source commit, package, versionCode and signing certificate.
   - iPhone and embedded Watch: `scripts/release/release.sh` for formal archive/export/upload, then `scripts/release/asc_testflight.py wait 45 <build number>`. Check purpose strings, bundle privacy and entitlements, and read back processing/group/review state separately from upload.
   - Other platforms use their existing private configuration, never an improvised credential or unsigned substitute.
5. Upload all verified products to the **same stable Release**, then verify manifest/source consistency, remote SHA-256 digests and independently downloaded bytes. Attach a publication receipt that distinguishes source, signature, store availability, attachment verification and production deployment.
6. Stable publication is not production Hub/site deployment, an App Store production release or a phone installation. Do not claim those without separate evidence.

## Independent beta every two hours

1. After the stable-work check, compare `origin/main` with the immutable beta currently identified by the authenticated `beta-channel` pointer. If it has not changed, report the skip rather than rebuilding the same commit.
2. Use a new `vX.Y.Z-beta.N` tag for the exact current `origin/main` commit and a GitHub **prerelease**. Never reuse an existing tag or turn it into the stable Release.
3. Build and verify the available formally signed platforms as above, but generate the CLI/Web manifest with `RELEASE_CHANNEL=beta`. The manifest must declare that beta version, channel and exact commit. Do not use a debug or unsigned APK/IPA/HAP.
4. Upload to the immutable prerelease, verify remote SHA-256 and independently download the bytes. Only then use `scripts/operations/beta_channel_release.py` with the verified receipt to advance the pointer's `latest.json` and `android.json`. It authenticates the previous pointer/source and permits forward source ancestry; it must never copy or replace binaries from another tag.
5. Stable `releases/latest` and the stable manifest stay unchanged. TestFlight beta is a separate store result and is not production deployment.

## Channel and platform boundaries

- Beta is **off by default**. With beta off, update only from the stable GitHub Release; with beta explicitly enabled, use only the beta feed and its immutable tagged artifacts.
- Switching channels clears candidates, caches and in-flight update state so a build from one channel cannot be installed as the other. Do not point a beta manifest at a moving pointer's binaries: the pointer identifies the immutable tag, and downloads resolve there.
- Report hub, Web, Android, iPhone/Watch TestFlight, Windows and HarmonyOS separately, with remote SHA-256 where an artifact exists. Do not call an incomplete platform successful.
- Windows native CLI archive verification requires the matching native Windows environment and its verification receipt. Formal HarmonyOS HAP requires DevEco and production signing. Electron installers require their signing/notarization or platform signing prerequisites. Keep these as explicit blockers, not reasons to publish unsigned/debug substitutes.
- Darwin command-line downloads are not stable Developer ID signatures unless the actual signing result proves it. Linux iPhone bridge helpers require their documented toolchain. WeChat is a separate extension, not a core release executable.

## When a build fails

Preserve the actual failure; never hide it by raising a budget, skipping required checks, weakening a regression assertion, moving a tag or substituting an older artifact.

- First distinguish disk/temporary-output corruption from a product/test failure. Only remove known rebuildable outputs after inspecting their ownership; preserve tracked source files, signing receipts and already verified remote artifacts. Do not run competing large builds into the same exhausted disk.
- For a real defect, deduplicate the issue, claim a fix in its own branch/worktree, add a focused regression and verify the affected suite before merging. A later repair release uses a new immutable version/commit, never a silently rewritten tag.
- If a same-day stable attempt is blocked, pause the next beta source selection and collaborate on the repair first. Do not start a duplicate platform build to conceal an incomplete release.

## Result report

For each platform, report **completed / processing / blocked** and the fixed commit. Include the manifest's SHA-256 and signed artifact digests where applicable, remote and independent-download verification, TestFlight processing and review state, and the final stable/beta pointers. State separately whether production deployment or phone installation was performed. Do not describe a prerelease upload as a production rollout.

---
name: scheduled-releases
description: Publish Persona stable releases at noon local time and independent immutable beta prereleases every two hours, with signed platform verification and separate update channels. Use when asked to run, schedule, resume or verify these daily/beta releases.
---

# Scheduled stable and beta releases

Compose the existing release tools; do not create a second release pipeline. This skill is for the release operator. It does **not** install a scheduler or publish anything by itself.

## Schedule and scope

- **Stable:** once per day at **12:00 in the operator's configured local time zone**. Do not silently interpret that as UTC or as another machine's zone.
- **Beta:** once every **2 hours**, never every 2 minutes, in the same explicitly configured zone.
- **Every release uses an explicit release PR.** The scheduler opens or refreshes the PR; it does not create a tag or publish locally. Use `release: vX.Y.Z` for stable and `release: vX.Y.Z-beta.N` for beta. `docs/operations/release.md` ("Release PRs and GitHub Actions publication") is the authoritative workflow.
- On an actual invocation, first inspect the latest `origin/main`, today's unfinished stable release PR and existing beta PRs. Complete or collaborate on unfinished stable work before starting a new beta. Never rebuild a platform that already completed for the same immutable merge result.
- Installing or reading this skill authorizes no publication, deployment, store submission or scheduler installation. Only the administrator's same-repository release PR can enter protected publication; an ordinary PR, fork PR or local tag cannot.
- The supported unattended default-branch worker is `scripts/operations/autorelease.py` with `scripts/operations/persona-autorelease.service` and `scripts/operations/persona-autorelease.timer`. Its target/path selection is not a beta scheduler. Do not run duplicate schedules or change an existing schedule just because this skill exists.

Read `docs/operations/release.md`, `scripts/operations/tag_release_prompt.md`, `docs/operations/hub-update.md`, `docs/operations/collaboration.md`, and the repository's super rules before acting. Follow issue → claim → branch → local verification → PR → merge for any repair or tooling change.

## Stable release at noon

1. Find duplicate and unfinished stable release issues, and verify the latest stable Release and tag. If an older attempt is incomplete, keep its tag immutable and repair in a new version, not by replacing its source or assets.
2. Compare the latest stable tag with current `origin/main`, including substantive feature commits that do not use the `feat` prefix:
   - New feature since the previous stable tag → increment the minor version and reset patch to zero.
   - Only fixes → increment the patch version.
   - No changes needing publication → report that the run is skipped.
3. Claim the issue with the selected exact commit, plan and ETA. Create or update the administrator's release PR with an unused immutable version. Never move an existing tag or publish a different branch/commit under it.
4. `release-pr.yml` verifies the proposed merge result on every available formally signed platform. Keep its failure visible and repair that release PR; do not merge a failing release PR, bypass a budget, or substitute a debug/unsigned artifact. CLI/Web has no signing secrets; protected platform jobs use only their required temporary configuration.
   - CLI/Web: the existing `publish.sh` with the release plan's fixed build number and `NO_DEPLOY=1`. It never deploys the website just to obtain artifacts.
   - Android: `scripts/release/android_tag_release.py` with the protected CI signer configuration. Verify tests, R8, source commit, package, versionCode and signing certificate.
   - iPhone and embedded Watch: `scripts/release/release.sh`, then `scripts/release/asc_testflight.py wait 45 <build number>`. Check purpose strings, bundle privacy, entitlements, processing, groups and external review separately.
   - Windows: run every `.exe.gz` on its matching native Windows architecture and require its verification receipt. The Electron installer separately requires Authenticode.
5. Only after every required platform check passes may the administrator merge the release PR. `release-publish.yml` rechecks the actual merge commit and plan, creates the immutable tag once, verifies the container and every uploaded artifact, independently downloads the bytes, then advances only the appropriate feed. A different merge commit needs new verification; a successful PR head does not carry over to it.
6. Stable publication is not production Hub/site deployment, an App Store production release or a phone installation. Do not claim those without separate evidence.

## Independent beta every two hours

1. After the stable-work check, compare `origin/main` with the immutable beta currently identified by the authenticated `beta-channel` pointer. If it has not changed, report the skip rather than rebuilding the same commit.
2. Open or refresh the administrator's release PR with a new `vX.Y.Z-beta.N` title for the exact current `origin/main` merge result and a GitHub **prerelease**. Never reuse an existing tag or turn it into the stable Release.
3. Let `release-pr.yml` verify the available formally signed platforms as above, with a beta manifest and fixed build number. Do not use a debug or unsigned APK/IPA/HAP. Merge only after every required platform passes.
4. The protected publication uploads to the immutable prerelease, verifies remote SHA-256 and independently downloads the bytes. Only then does it use `scripts/operations/beta_channel_release.py` with the verified receipt to advance the pointer's `latest.json` and `android.json`. It authenticates the previous pointer/source and permits forward source ancestry; it never copies or replaces binaries from another tag.
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

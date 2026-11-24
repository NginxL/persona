#!/bin/bash
# Public, local-runnable Apple checks. Requires XcodeGen, Python 3 and rsync.
# The default lane uses macOS 26 and Xcode 26.0.x; ios18 uses the 18.6 simulator.
# Usage: scripts/ci/apple.sh [all|ios|ios18|packages|ios-build|ios-focused|ios-regressions|ios-model-picker|ios-ipad-diagnostic]
# Cache CI_SPM_CACHE (default ~/Library/Caches/persona-ci/spm); keep the printed
# output directory's *.xcresult bundles when investigating failures.
set -euo pipefail
source "$(dirname "$0")/common.sh"
LANE="${1:-all}"
case "$LANE" in all|ios|ios18|packages|ios-build|ios-focused|ios-regressions|ios-model-picker|ios-ipad-diagnostic) ;; *) printf 'Usage: %s [all|ios|ios18|packages|ios-build|ios-focused|ios-regressions|ios-model-picker|ios-ipad-diagnostic]\n' "$0" >&2; exit 2 ;; esac
[ "$#" -le 1 ] || { printf 'apple-ci: too many arguments\n' >&2; exit 2; }
[ "$(uname -s)" = Darwin ] || { printf 'apple-ci: FAIL requires macOS with Xcode\n' >&2; exit 1; }
for tool in python3 xcodebuild xcrun xcodegen rsync git; do
  command -v "$tool" >/dev/null || { printf 'apple-ci: FAIL missing %s\n' "$tool" >&2; exit 1; }
done
# Never silently fall back to the hosted image's newer default compiler.
if [ "$LANE" = ios18 ]; then
  export DEVELOPER_DIR="${DEVELOPER_DIR:-/Applications/Xcode_26.3.app/Contents/Developer}"
else
  export DEVELOPER_DIR="${DEVELOPER_DIR:-/Applications/Xcode_26.0.1.app/Contents/Developer}"
fi
XCODE_VERSION=$(xcodebuild -version)
printf '%s\n' "$XCODE_VERSION"
if [ "$LANE" = ios18 ]; then
  # A local newer compiler can run the same 18.6 binary checks; CI selects 26.3.
  XCODE_MAJOR=$(sed -n 's/^Xcode \([0-9]*\).*/\1/p' <<< "$XCODE_VERSION")
  [ "${XCODE_MAJOR:-0}" -ge 26 ] || { printf 'apple-ci: FAIL Xcode 26 or newer is required\n' >&2; exit 1; }
else
  case "$XCODE_VERSION" in 'Xcode 26.0'*$'\n'*) ;; *) printf 'apple-ci: FAIL select Xcode 26.0.x explicitly\n' >&2; exit 1 ;; esac
  MACOS_VERSION=$(sw_vers -productVersion)
  [ "${MACOS_VERSION%%.*}" -ge 26 ] || { printf 'apple-ci: FAIL macOS 26 is needed to run the Mac/PersonaKit tests\n' >&2; exit 1; }
fi
xcrun swift --version
xcodegen --version

OUTPUT=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/persona-apple-ci.XXXXXX")
DIAGNOSTICS="${CI_APPLE_DIAGNOSTICS:-$OUTPUT/diagnostics}"
SOURCE="$OUTPUT/source"
PROJECT="$SOURCE/apple/Persona.xcodeproj"
OFFLINE_PROJECT="$SOURCE/apple/PersonaOfflineUnitTests.xcodeproj"
SPM="${CI_SPM_CACHE:-$HOME/Library/Caches/persona-ci/spm}"
mkdir -p "$SOURCE" "$SPM"
SPM=$(cd "$SPM" && pwd)
SIM=""
SIM_STARTED=0
SIM_NAME="Persona CI $(basename "$OUTPUT")"
REGULAR_SIM=""
REGULAR_SIM_STARTED=0
REGULAR_SIM_NAME="$SIM_NAME Regular"
cleanup_device() {
  local udid="$1" name="$2" started="$3"
  # A failed creation may already have created the device without returning its ID.
  if [ -z "$udid" ] && [ "$started" = 1 ]; then
    udid=$(python3 -I - "$name" <<'PY'
import json, subprocess, sys
try:
    data = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "devices", "-j"], text=True, timeout=60))
    hits = [d["udid"] for devices in data["devices"].values() for d in devices if d["name"] == sys.argv[1]]
    if len(hits) == 1:
        print(hits[0])
except (OSError, ValueError, subprocess.SubprocessError) as error:
    print(f"apple-ci: could not locate owned simulator for cleanup: {error}", file=sys.stderr)
PY
    ) || true
  fi
  if [ -n "$udid" ]; then
    ci_run 'Shut down owned simulator' 120 xcrun simctl shutdown "$udid" || true
    ci_run 'Delete owned simulator' 120 xcrun simctl delete "$udid" || true
  fi
}
simulator_udid() {
  # Accept only the helper's single UDID, never a partial/error log.
  python3 -I - "$1" <<'PY'
import re, sys
matches = re.findall(r"(?m)^([0-9A-Fa-f]{8}-(?:[0-9A-Fa-f]{4}-){3}[0-9A-Fa-f]{12})$", open(sys.argv[1]).read())
if len(matches) != 1:
    sys.exit("apple-ci: FAIL simulator helper did not return exactly one UDID")
print(matches[0])
PY
}
cleanup() {
  local code=$?
  trap - EXIT
  if [ "$code" -ne 0 ]; then
    # Capture simulator state before cleanup; only allowlisted logs and xcresult
    # bundles leave the disposable output, never synthetic/private config or keys.
    local scope=all
    if [ "$LANE" = ios-ipad-diagnostic ]; then
      scope=regular
    fi
    ci_run 'Collect Apple failure diagnostics' 180 python3 "$CI_ROOT/scripts/ci/apple_diagnostics.py" \
      "$OUTPUT" "$DIAGNOSTICS" --scope "$scope" || printf 'apple-ci: failure diagnostics could not be collected\n' >&2
  fi
  cleanup_device "$SIM" "$SIM_NAME" "$SIM_STARTED"
  cleanup_device "$REGULAR_SIM" "$REGULAR_SIM_NAME" "$REGULAR_SIM_STARTED"
  printf 'apple-ci: %s (exit %s); evidence: %s\n' "$([ "$code" = 0 ] && printf PASS || printf FAIL)" "$code" "$OUTPUT"
  exit "$code"
}
trap cleanup EXIT
printf 'apple-ci: output %s; SwiftPM cache %s\n' "$OUTPUT" "$SPM"
# No source imports CallKit and no build setting links it, before anything is built (#9290, docs/operations/release.md
# "No CallKit"); the built apps' binaries are checked after each build below.
ci_run 'No CallKit in Apple sources and build settings' 120 python3 "$CI_ROOT/scripts/test/test_apple_no_callkit.py"

# XcodeGen writes tracked plists/entitlements, and the normal config helper writes
# Config.xcconfig. Generate in a snapshot, not over a developer's private files.
# git's file list retains local source edits but excludes ignored build/config
# files. Copy all repository inputs: source-based tests read schema/, spec/,
# Android design tokens, the release script and the hub's persona assets too.
git ls-files --cached --others --exclude-standard -z -- . ':(exclude).ci-cache/**' |
  python3 -I -c 'import os, sys; root = sys.argv[1]; paths = sys.stdin.buffer.read().split(b"\0"); sys.stdout.buffer.write(b"".join(p + b"\0" for p in paths if p and os.path.lexists(os.path.join(os.fsencode(root), p))))' \
    "$CI_ROOT" > "$OUTPUT/inputs"
ci_run 'Snapshot Apple source and test fixtures' 120 rsync -a --from0 --files-from="$OUTPUT/inputs" "$CI_ROOT/" "$SOURCE/"
export PERSONA_CONFIG="$OUTPUT/config.json"
python3 -I - "$PERSONA_CONFIG" <<'PY'
import json, sys
with open(sys.argv[1], "w") as output:
    json.dump({"app": {"bundle_id": "org.example.persona.ci", "team_id": ""}}, output)
PY
# These are explicitly public synthetic values; persona_config never opens the
# developer's config. DEVELOPER_DIR already selected above wins in its env helper.
export PERSONA_META_WEARABLES=NO
# The whole project, with the watch app and the share extension (apple/lean-dev-build.yml is sim.sh's alone).
unset PERSONA_LEAN_DEV_BUILD
ci_run 'Stage resolved packages for Meta configuration' 60 python3 "$CI_ROOT/scripts/ci/apple_resolved.py" \
  --tracked "$CI_ROOT/apple/Packages/PersonaSSH/Package.resolved" \
  --staged "$SOURCE/apple/Packages/PersonaSSH/Package.resolved" \
  --meta "$PERSONA_META_WEARABLES" --evidence "$DIAGNOSTICS/resolved-variant.txt"
if [ "$LANE" = ios18 ]; then
  ci_run 'Build PersonaCore for the simulator' 1200 "$SOURCE/native/app-core/scripts/build-apple.sh" --simulator
else
  ci_run 'Build PersonaCore XCFramework before package resolution' 1200 "$SOURCE/native/app-core/scripts/build-apple.sh"
fi
ci_run 'Generate public unsigned Apple config' 60 python3 "$SOURCE/scripts/persona_config.py" xcconfig
ci_run 'Generate Apple project' 120 xcodegen generate --spec "$SOURCE/apple/project.yml" --project "$SOURCE/apple"
[ -s "$SOURCE/apple/PersonaTests/TokenBaseline.json" ] || { printf 'apple-ci: FAIL committed token baseline missing\n' >&2; exit 1; }

UNSIGNED=(CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO CODE_SIGN_IDENTITY= DEVELOPMENT_TEAM=)
# Runnable hosts use local ad-hoc signatures, not distribution signatures.
# Unsigned simulator runs are not equivalent for Keychain/persistence tests
# (docs/lessons/testing.md #6192). No certificate/profile or secret is required.
TEST_SIGNING=(CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER=)
XCODE=(xcodebuild -project "$PROJECT" -configuration Debug -clonedSourcePackagesDirPath "$SPM/SourcePackages")
resolve() {
  ci_run "Resolve Swift packages ($1)" 900 "${XCODE[@]}" -scheme "$1" -resolvePackageDependencies
}
result() {
  local name="$1" device="$2"; shift 2
  if [ "$LANE" = ios18 ]; then
    ci_run "Inspect test results ($name)" 120 python3 "$CI_ROOT/scripts/ci/apple_result.py" "$OUTPUT/$name.xcresult" \
      "$@" --ios18 "$device" "$OUTPUT/$name.log" --xcode-status "$IOS_LAST_STATUS" || IOS_FAILED=1
  else
    ci_run "Inspect test results ($name)" 120 python3 "$CI_ROOT/scripts/ci/apple_result.py" "$OUTPUT/$name.xcresult" "$@"
  fi
}
test_batch() {
  local name="$1"; shift
  if [ "$LANE" = ios18 ]; then
    # Keep xcodebuild's exit status and inspect the log even when it failed.
    IOS_LAST_STATUS=0
    ci_run "$@" 2>&1 | tee "$OUTPUT/$name.log" || IOS_LAST_STATUS=$?
  else
    # pipefail keeps the bounded command's failure status. The existing failure
    # collector retains this allowlisted console log with its xcresult.
    ci_run "$@" 2>&1 | tee "$OUTPUT/$name.log"
  fi
}

if [ "$LANE" = all ] || [ "$LANE" = ios ] || [ "$LANE" = packages ]; then
  # Real loopback sshd and mock hub, with temporary keys: exercise tunnel recovery before the app builds (#11671).
  ci_run 'PersonaSSH native tunnel integration tests' 900 env PERSONA_SSH_TRACE="$OUTPUT/native-ssh-trace.log" xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaSSH" --scratch-path "$SPM/PersonaSSH"
  ci_run 'PersonaCore strict-concurrency tests and C-binding fixture replay' 900 xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaCore" --scratch-path "$SPM/PersonaCore" -Xswiftc -warnings-as-errors
  # The wire layer every app target links, the watch and widgets included (#7895). -warnings-as-errors on the command line
  # also applies to a package's dependencies, so PersonaWire builds without warnings for the packages built on it (#12734).
  ci_run 'PersonaWire unit tests' 300 xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaWire" --scratch-path "$SPM/PersonaWire" -Xswiftc -warnings-as-errors
  ci_run 'PersonaKit unit tests' 900 xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaKit" --scratch-path "$SPM/PersonaKit"
  # The live screen's video wire and capture decisions (#3954): PersonaScreenCore, all Persona Link's helper is built from
  # (#7927), and PersonaScreenKit, the iOS app's side on top of it (route session, quality tiers).
  ci_run 'PersonaScreenCore unit tests' 300 xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaScreenCore" --scratch-path "$SPM/PersonaScreenCore" -Xswiftc -warnings-as-errors
  ci_run 'PersonaScreenKit unit tests' 300 xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaScreenKit" --scratch-path "$SPM/PersonaScreenKit" -Xswiftc -warnings-as-errors
  # The pixel scroll record (#7775), shared by Persona Input and the iOS app, and Persona Input itself, a package apart
  # from Persona Screen's.
  ci_run 'PersonaInputKit unit tests' 300 xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaInputKit" --scratch-path "$SPM/PersonaInputKit" -Xswiftc -warnings-as-errors
  # The module contract and registry (#13432, docs/apple/ios.md "Modules").
  ci_run 'PersonaModuleAPI unit tests' 300 xcrun swift test --package-path "$SOURCE/apple/Packages/PersonaModuleAPI" --scratch-path "$SPM/PersonaModuleAPI" -Xswiftc -warnings-as-errors
  ci_run 'Persona Input build' 600 xcrun swift build -c release --package-path "$SOURCE/apple/PersonaLink/Input" --scratch-path "$SPM/PersonaInput" -Xswiftc -warnings-as-errors
  # The independent Link package contains only Persona Screen, built as mac-link.sh builds it; PersonaScreenCore's tests
  # cover its capture decisions. There is no menu-bar app or separate Link UI test target to build.
  ci_run 'Persona Screen build' 600 xcrun swift build -c release --package-path "$SOURCE/apple/PersonaLink" --scratch-path "$SPM/PersonaScreen" --product PersonaScreen -Xswiftc -warnings-as-errors
  # Blocking calls on the main actor (docs/apple/performance.md, "Automated checks"). The lint is built in a fixed
  # folder beside its build output: the snapshot above moves on every run, and a package whose folder moved is rebuilt
  # from scratch, swift-syntax included (seen with Xcode 27.2). Unchanged files keep their times (no -t), so an
  # unchanged lint is not rebuilt at all. One release build serves the tests and the check (no testable imports).
  mkdir -p "$SPM/IsolationLint/package"
  ci_run 'Stage isolation lint package' 60 rsync -rlp --checksum --delete "$SOURCE/scripts/test/isolation-lint/" "$SPM/IsolationLint/package/"
  ci_run 'Isolation lint unit tests' 900 xcrun swift test -c release --disable-testable-imports --package-path "$SPM/IsolationLint/package" --scratch-path "$SPM/IsolationLint/build"
  ci_run 'Blocking calls on the main actor (isolation lint)' 300 "$SPM/IsolationLint/build/release/isolation-lint" --root "$SOURCE" --baseline "$SOURCE/scripts/test/apple_isolation_baseline.json"
fi
if [ "$LANE" = all ] || [ "$LANE" = ios ] || [ "$LANE" = ios-build ] || [ "$LANE" = ios-focused ] || [ "$LANE" = ios-regressions ] || [ "$LANE" = ios-model-picker ] || [ "$LANE" = ios-ipad-diagnostic ]; then
  for sdk in iphoneos iphonesimulator watchos watchsimulator; do
    ci_run "Require $sdk SDK" 60 xcrun --sdk "$sdk" --show-sdk-version
  done
  # Generic device builds require the matching platform support even when no
  # simulator is created. Keep the pinned runtime preparation in every iOS lane.
  # Current hosted images omit the Xcode 26.0-compatible iOS/watch runtimes.
  # Generic device builds validate both platforms for the embedded watch app,
  # even though SDK lookup succeeds. Prepare both before resolving/building.
  # The pinned Xcode selects compatible patch runtimes (such as iOS 26.0.1).
  # Validate 26.0.x explicitly; never substitute a newer minor or compiler.
  for platform in iOS watchOS; do
    # This focused lane runs no Watch or device batch, so it prepares only iOS.
    if [ "$LANE" = ios-model-picker ] && [ "$platform" = watchOS ]; then
      continue
    fi
    runtimes="$OUTPUT/runtimes-$platform-before.json"
    ci_run "List $platform simulator runtimes" 120 bash -c 'xcrun simctl list runtimes -j > "$1"' -- "$runtimes"
    if ! runtime=$(python3 "$CI_ROOT/scripts/ci/apple_runtime.py" "$runtimes" --platform "$platform"); then
      ci_run "Download matching $platform 26.0 simulator runtime" 1200 xcodebuild -downloadPlatform "$platform" -architectureVariant universal
      runtimes="$OUTPUT/runtimes-$platform-after.json"
      ci_run "List downloaded $platform simulator runtimes" 120 bash -c 'xcrun simctl list runtimes -j > "$1"' -- "$runtimes"
      runtime=$(python3 "$CI_ROOT/scripts/ci/apple_runtime.py" "$runtimes" --platform "$platform")
    fi
    printf 'apple-ci: selected compatible %s runtime %s\n' "$platform" "$runtime"
    if [ "$platform" = iOS ]; then IOS_RUNTIME="$runtime"; fi
  done
  resolve Persona
  if [ "$LANE" != ios-model-picker ]; then
  # Generic device builds compile the actual watch/widget/notification/share
  # dependency graph; do not delete the watch target to accommodate the runner.
  ci_run 'Unsigned iOS and embedded watch/extensions build' 1800 "${XCODE[@]}" -scheme Persona \
    -destination 'generic/platform=iOS' -derivedDataPath "$OUTPUT/ios-device" \
    -disableAutomaticPackageResolution "${UNSIGNED[@]}" build
  # Every Mach-O in the device app (extensions, frameworks, the Watch app) and every architecture: none links CallKit,
  # weakly or through a package (#9290). Device-only linker flags exist, so the simulator build cannot stand in for it.
  ci_run 'No CallKit in the device build' 300 python3 "$CI_ROOT/scripts/release/check_no_callkit.py" \
    "$OUTPUT/ios-device/Build/Products/Debug-iphoneos/Persona.app"
  # A phone's main thread has a 1 MB stack, the simulator's 8 MB: view functions with big fixed frames in this Debug
  # device build fail here (#6735; MainPagesStackTests measures the run-time part in the simulator).
  ci_run 'Debug view stack frames (device build)' 300 python3 "$CI_ROOT/scripts/ci/apple_stack_frames.py" \
    "$OUTPUT/ios-device/Build/Products/Debug-iphoneos/Persona.app"
  # The app without its moved feature modules (apple/modules-core-only.yml, #14017; docs/apple/ios.md "Modules"): the
  # core must build with every apple/Modules/<Name> left out. Generated into the same project, built, then the whole
  # project generated again for the steps below. Only the app scheme: the watch and extensions compile no module.
  ci_run 'Generate core-only Apple project' 120 env PERSONA_CORE_ONLY=YES xcodegen generate --spec "$SOURCE/apple/project.yml" --project "$SOURCE/apple"
  ci_run 'Core-only iOS build' 1800 "${XCODE[@]}" -scheme Persona \
    -destination 'generic/platform=iOS' -derivedDataPath "$OUTPUT/ios-core-only" \
    -disableAutomaticPackageResolution "${UNSIGNED[@]}" build
  ci_run 'Generate Apple project again (whole)' 120 xcodegen generate --spec "$SOURCE/apple/project.yml" --project "$SOURCE/apple"
  fi
fi
if [ "$LANE" = ios18 ]; then
  runtimes="$OUTPUT/runtimes-iOS-before.json"
  ci_run 'List installed simulator runtimes' 120 bash -c 'xcrun simctl list runtimes -j > "$1"' -- "$runtimes"
  if ! IOS_RUNTIME=$(python3 "$CI_ROOT/scripts/ci/apple_runtime.py" "$runtimes" --version 18.6); then
    ci_run 'Download iOS 18.6 simulator runtime' 1200 xcodebuild -downloadPlatform iOS -buildVersion 18.6
    runtimes="$OUTPUT/runtimes-iOS-after.json"
    ci_run 'List downloaded simulator runtimes' 120 bash -c 'xcrun simctl list runtimes -j > "$1"' -- "$runtimes"
    IOS_RUNTIME=$(python3 "$CI_ROOT/scripts/ci/apple_runtime.py" "$runtimes" --version 18.6)
  fi
  resolve Persona
fi
if [ "$LANE" != packages ] && [ "$LANE" != ios-build ]; then
  if [ "$LANE" = ios18 ]; then
    IOS_VERSION=18.6
    IOS_PHONE="iPhone 16 Pro"
    IOS_TABLET="iPad (10th generation)"
    BUILD_SECONDS=3600
    UNIT_SECONDS=3600
  else
    IOS_VERSION=26.0
    IOS_PHONE="iPhone 17 Pro"
    IOS_TABLET="iPad Pro 11-inch (M5)"
    BUILD_SECONDS=1800
    UNIT_SECONDS=1800
  fi
  IOS_COMMON=(xcodebuild -project "$OFFLINE_PROJECT" -configuration Debug \
    -clonedSourcePackagesDirPath "$SPM/SourcePackages" -scheme PersonaOfflineUnitTests -derivedDataPath "$OUTPUT/ios-tests" \
    -parallel-testing-enabled NO -disableAutomaticPackageResolution "${TEST_SIGNING[@]}")
  # Explicit fixture-only exclusions, not evidence of those integrations passing.
  # Swift Testing method selectors include (); without it disabled integration
  # cases remain selected and appear as unexpected skips in the result bundle.
  IOS_EXCLUSIONS=(
    -skip-testing:PersonaTests/OldHubCompatibilityIntegrationTests
    "-skip-testing:PersonaTests/NotificationServiceTests/opensWhatTheRelayDelivered()"
    "-skip-testing:PersonaTests/VideoFileTests/nativeVideoPlaysSeeksAndCleansUp()"
    "-skip-testing:PersonaTests/VideoFileResourceTests/realHTTPReadsMiddleTailAndRejectsChangedRevision()"
  )
  printf '%s\n' 'apple-ci: excluded integration cases: old-hub lab; relay/APNs payload lab; native video fixture; real video HTTP fixture; destructive HTML loop (dedicated external watchdog required). UI tests are a separate mock-hub/screenshot lane.'
  # WebKit suites must run in separate batches, not interleave in Swift Testing
  # despite @MainActor or xcodebuild's parallel-testing switch (#2025).
  # Compile before creating the device: compilation cannot consume its readiness deadline.
  # The full execution retains exactly the same suite selection.
  # #11289's real .regular foreground scene cannot run on this lane's iPhone.
  # It is neither skipped nor replaced with an overridden trait: a separate
  # real-iPad test-without-building lane below runs the unchanged pixel test.
  if [ "$LANE" = ios-focused ] || [ "$LANE" = ios-ipad-diagnostic ]; then
  # Explicit diagnostic suites after the same build/readiness gates as ios.
  # The ordinary lane below is unchanged and keeps every selection/assertion.
  IOS_UNIT_SELECTION=(-only-testing:PersonaTests/PageSearchableTests
    -only-testing:PersonaTests/StarredMessagesTests
    -only-testing:PersonaTests/LauncherModelChoiceTests
    -only-testing:PersonaTests/OmpNewAgentTests
    -only-testing:PersonaTests/PaseoPermissionModesTests
    -only-testing:PersonaTests/PaseoProvidersTests
    -only-testing:PersonaTests/AboutVersionCompactVisualTests)
else
  IOS_UNIT_SELECTION=("${IOS_EXCLUSIONS[@]}" -only-testing:PersonaTests
    -skip-testing:PersonaTests/AboutVersionRegularVisualTests
    -skip-testing:PersonaTests/AssistantHTMLCardTests -skip-testing:PersonaTests/AssistantHTMLSecurityTests)
fi
  if [ "$LANE" = ios-model-picker ]; then
  # Run the two merged Assistant switching suites, then stop before unrelated batches.
  IOS_UNIT_SELECTION=(-only-testing:PersonaTests/AssistantModelChoiceTests
    -only-testing:PersonaTests/AssistantModelPanelModelTests)
fi
  if [ "$LANE" = ios-regressions ]; then
  # Run only the two remaining regressions after the shared build/readiness gates.
  IOS_UNIT_SELECTION=(-only-testing:PersonaTests/MainPagesStackTests
    -only-testing:PersonaTests/PhoneSystemToolsTests)
fi
  # The same real Persona test host and all of PersonaTests, but no second
  # compilation of the embedded Watch/Share targets already covered by the
  # device build. Resolve the ordinary project once, then remove only targets
  # the offline test host cannot reach. The default/UI/About projects are whole.
  ci_run 'Generate iPhone-only offline unit-test project' 120 bash -c '
    set -euo pipefail
    spec="$1"; output="$2"
    xcodegen dump --spec "$spec" --type json > "$output/Persona.whole.json"
    python3 "$3" "$output/Persona.whole.json" "$output/PersonaOfflineUnitTests.json"
  ' -- "$SOURCE/apple/project.yml" "$OUTPUT" "$CI_ROOT/scripts/ci/apple_offline_project.py"
  ci_run 'Generate offline unit-test Xcode project' 120 xcodegen generate \
    --spec "$OUTPUT/PersonaOfflineUnitTests.json" --project-root "$SOURCE/apple" \
    --project "$SOURCE/apple"
  # A generic destination must retain the host's single runnable architecture.
  ci_run 'Build iOS offline unit tests' "$BUILD_SECONDS" bash -c '
    set -euo pipefail
    ci_root="$1"; output="$2"; seconds="$3"; shift 3
    watch_log="$output/diagnostics/build-resources"
    mkdir -p "$watch_log"
    printf "%s\n" "{\"unavailable\":\"observer did not complete; no memory-pressure verdict\"}" > "$watch_log/final.pending"
    mv "$watch_log/final.pending" "$watch_log/final.json"
    printf "%s\n" "observer incomplete; bounded unavailable final evidence only" > "$watch_log/observer.status.txt"
    python3 "$ci_root/scripts/ci/apple_build_watch.py" \
      --output "$watch_log" --stop "$watch_log/stop" --seconds "$seconds" \
      --derived-data "$output/ios-tests" &
    watcher=$!
    stop_watcher() {
      local code=$?
      trap - EXIT TERM INT
      : > "$watch_log/stop"
      # A short stop drain covers normal completion. The observer reaper bounds
      # final collection and kills its detached process group if needed.
      for _ in 1 2 3 4 5 6 7 8 9 10; do
        kill -0 "$watcher" 2>/dev/null || break
        sleep 0.05
      done
      if kill -0 "$watcher" 2>/dev/null; then
        kill -KILL "-$watcher" 2>/dev/null || kill -KILL "$watcher" 2>/dev/null || true
      fi
      wait "$watcher" || true
      exit "$code"
    }
    trap stop_watcher EXIT TERM INT
    "$@"
  ' -- "$CI_ROOT" "$OUTPUT" "$BUILD_SECONDS" "${IOS_COMMON[@]}" \
    -destination 'generic/platform=iOS Simulator' ARCHS="$(uname -m)" ONLY_ACTIVE_ARCH=YES \
    "${IOS_UNIT_SELECTION[@]}" build-for-testing
  ci_run 'No CallKit in the simulator build' 300 python3 "$CI_ROOT/scripts/release/check_no_callkit.py" \
    "$OUTPUT/ios-tests/Build/Products/Debug-iphonesimulator/Persona.app"

  # Split the former 1000-second preparation budget into creation (100) and real
  # hosted readiness (900). A device state or running service is not a test gate.
  # sim-boot remains the creation entry, with a fresh stock simulator (not
  # paired to a real hub), pinned to the validated compiler-compatible runtime.
  SIM_STARTED=1
  ci_run "Create stock iOS $IOS_VERSION test simulator" 100 bash -c \
    '"$1" --create-only --profile stock --runtime "$4" --device-type "$6" "$2" > "$3" 2> >(tee "$5" >&2)' \
    -- "$SOURCE/scripts/app/sim-boot.sh" "$SIM_NAME" "$OUTPUT/simulator.log" "$IOS_RUNTIME" "$OUTPUT/simulator-boot.log" "$IOS_PHONE"
  # Accept only the helper's single UDID, never a partial/error log.
  SIM=$(simulator_udid "$OUTPUT/simulator.log")
  IOS_TESTS=("${IOS_COMMON[@]}" -destination "platform=iOS Simulator,id=$SIM" -destination-timeout 120)
  # Xcode owns boot, installation and test-host launch. The existing hosted suite
  # and its strict result check must both finish inside the readiness deadline.
  CI_BUNDLE_ID=$(python3 -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["app"]["bundle_id"])' "$PERSONA_CONFIG")
  python3 "$CI_ROOT/scripts/ci/apple_launch_diagnostics.py" \
    --record "$OUTPUT/simulator-readiness-context.json" --udid "$SIM" --bundle-id "$CI_BUNDLE_ID"
  ci_run 'Verify hosted iOS test readiness' 900 bash -c '
    set -euo pipefail
    checker="$1"; bundle="$2"; log="$3"; watch_log="$4"; udid="$5"; ci_root="$6"; bundle_id="$7"; output="$8"; lane="$9"; shift 9
    if [ "$lane" = ios18 ]; then
      status=0
      "$@" -only-testing:PersonaTests/TypeScaleTests -resultBundlePath "$bundle" test-without-building 2>&1 | tee "$log" || status=$?
      python3 "$checker" "$bundle" TypeScaleTests --ios18 "$udid" "$log" --xcode-status "$status"
      exit "$status"
    fi
    mkdir -p "$watch_log"
    printf "%s\n" "collection incomplete; bounded unavailable evidence retained; no launch cause established" > "$watch_log/observer.status.pending"
    mv "$watch_log/observer.status.pending" "$watch_log/observer.status.txt"
    printf "%s\n" "{\"unavailable\":\"observer did not complete; no host-process verdict\"}" > "$watch_log/host-final.pending"
    mv "$watch_log/host-final.pending" "$watch_log/host-final.json"
    for product in 0 1 2; do
      printf "%s\n" "unavailable: product collection incomplete" > "$watch_log/product-$product-architecture.pending"
      mv "$watch_log/product-$product-architecture.pending" "$watch_log/product-$product-architecture.txt"
      printf "%s\n" "incomplete" > "$watch_log/product-$product-architecture.status.pending"
      mv "$watch_log/product-$product-architecture.status.pending" "$watch_log/product-$product-architecture.txt.status.txt"
      printf "%s\n" "unavailable: product collection incomplete" > "$watch_log/product-$product-libraries.pending"
      mv "$watch_log/product-$product-libraries.pending" "$watch_log/product-$product-libraries.txt"
      printf "%s\n" "incomplete" > "$watch_log/product-$product-libraries.status.pending"
      mv "$watch_log/product-$product-libraries.status.pending" "$watch_log/product-$product-libraries.txt.status.txt"
    done
    for product in 0 1; do
      printf "%s\n" "unavailable: bundle signature collection incomplete" > "$watch_log/product-$product-bundle-signature.pending"
      mv "$watch_log/product-$product-bundle-signature.pending" "$watch_log/product-$product-bundle-signature.txt"
      printf "%s\n" "incomplete" > "$watch_log/product-$product-bundle-signature.status.pending"
      mv "$watch_log/product-$product-bundle-signature.status.pending" "$watch_log/product-$product-bundle-signature.txt.status.txt"
    done
    # Noninteractive Bash gives the observer a separate process group only with
    # job control enabled. The bounded runner must never signal this group.
    set -m
    python3 "$ci_root/scripts/ci/apple_readiness_watch.py" \
      --udid "$udid" --bundle-id "$bundle_id" --scope-root "$output" \
      --app "$output/ios-tests/Build/Products/Debug-iphonesimulator/Persona.app/Persona" \
      --test-bundle "$output/ios-tests/Build/Products/Debug-iphonesimulator/Persona.app/PlugIns/PersonaTests.xctest/PersonaTests" \
      --output "$watch_log" --seconds 900 &
    watcher=$!
    set +m
    stop_watcher() {
      local code=$?
      trap - EXIT TERM INT
      # A fast probe may finish while the product checks are still running.
      # Give those bounded commands the existing short drain before asking the
      # observer to stop its polling, then let final evidence finish naturally.
      for _ in $(seq 1 50); do
        [ -e "$watch_log/product-ready" ] && break
        kill -0 "$watcher" 2>/dev/null || break
        sleep 0.1
      done
      : > "$watch_log/observer.stop"
      # This observer has its own process group, so the readiness timeout does
      # not kill its evidence. Send the stop to that group explicitly: waiting
      # without it would leave the observer collecting until the full horizon.
      kill -TERM "-$watcher" 2>/dev/null || kill -TERM "$watcher" 2>/dev/null || true
      # The observer stops polling after its bounded product phase, then
      # completes final evidence. The outer 900-second deadline still owns a
      # stuck watcher; never kill it before that final collection.
      wait "$watcher" || true
      # Close any remaining descendant after the watcher has finished.
      kill -KILL "-$watcher" 2>/dev/null || true
      wait "$watcher" || true
      exit "$code"
    }
    trap stop_watcher EXIT TERM INT
    {
      "$@" -only-testing:PersonaTests/TypeScaleTests -resultBundlePath "$bundle" test-without-building
      python3 "$checker" "$bundle" TypeScaleTests
    } 2>&1 | tee "$log"
  ' -- "$CI_ROOT/scripts/ci/apple_result.py" "$OUTPUT/ios-readiness.xcresult" "$OUTPUT/simulator-readiness.log" "$OUTPUT/diagnostics/live-readiness" "$SIM" "$CI_ROOT" "$CI_BUNDLE_ID" "$OUTPUT" "$LANE" "${IOS_TESTS[@]}"

  # The real EventKit round-trip uses only this disposable simulator's store.
  # TCC grants can precede installation of the synthetic CI app.
  # On a just-booted stock simulator the grant waits for the device's own services: 54.5 s on a passing hosted run,
  # over 60 s on a failing one (#1402). A single attempt with room to spare; a hang still ends the lane.
  # A successful Xcode test can leave its destination shut down. Start only our device if needed;
  # boot initiation and the single required TCC grant share the original permission deadline.
  ci_run 'Grant Reminders access to the owned test app' 300 bash -c '
    set -euo pipefail
    "$1" --start-only --profile stock "$2" >/dev/null
    xcrun simctl privacy "$2" grant reminders "$3"
  ' -- "$SOURCE/scripts/app/sim-boot.sh" "$SIM" "$CI_BUNDLE_ID"
  export TEST_RUNNER_PERSONA_EVENTKIT_ROUNDTRIP=1
  # The ordinary Persona scheme also runs the compact visual comparisons. Unlike
  # the dedicated AboutVersionVisualTests scheme, supply their required evidence
  # directory here, using only this disposable run's output, never a shared one.
  export TEST_RUNNER_PERSONA_SHOTS="$OUTPUT/shots"

  IOS_FAILED=0
  if [ "$LANE" = ios18 ]; then
    # These unit suites require real compact/regular scenes and screenshot output.
    # Both execute below, in the language and region of AboutVersionVisualTests.
    IOS_UNIT_SELECTION+=(-skip-testing:PersonaTests/AboutVersionCompactVisualTests)
  fi
  test_batch ios-unit 'iOS offline unit and design/token tests' "$UNIT_SECONDS" "${IOS_TESTS[@]}" "${IOS_UNIT_SELECTION[@]}" \
    -resultBundlePath "$OUTPUT/ios-unit.xcresult" test-without-building
  if [ "$LANE" = ios-focused ] || [ "$LANE" = ios-ipad-diagnostic ]; then
    result ios-unit "$SIM" PageSearchableTests StarredMessagesTests LauncherModelChoiceTests OmpNewAgentTests PaseoPermissionModesTests PaseoProvidersTests AboutVersionCompactVisualTests
  elif [ "$LANE" != ios-regressions ] && [ "$LANE" != ios-model-picker ]; then
    result ios-unit "$SIM" TypeScaleTests IconVocabularyTests TokenRatchetTests StyleSpecTests
  fi
  if [ "$LANE" = ios-model-picker ]; then
    result ios-unit "$SIM" AssistantModelChoiceTests AssistantModelPanelModelTests
    exit "$IOS_FAILED"
  fi
  if [ "$LANE" = ios-regressions ]; then
    result ios-unit "$SIM" MainPagesStackTests PhoneSystemToolsTests
    exit "$IOS_FAILED"
  fi
  if [ "$LANE" != ios-focused ]; then
  test_batch ios-html-card 'iOS HTML card tests (separate batch)' 600 "${IOS_TESTS[@]}" \
    -only-testing:PersonaTests/AssistantHTMLCardTests -resultBundlePath "$OUTPUT/ios-html-card.xcresult" test
  result ios-html-card "$SIM" AssistantHTMLCardTests
  test_batch ios-html-security 'iOS bounded HTML security tests (separate batch)' 600 "${IOS_TESTS[@]}" \
    -only-testing:PersonaTests/AssistantHTMLSecurityTests \
    "-skip-testing:PersonaTests/AssistantHTMLSecurityTests/aRealInfiniteLoopCanBeDiscardedWithoutBlockingANewCard()" \
    -resultBundlePath "$OUTPUT/ios-html-security.xcresult" test
  result ios-html-security "$SIM" AssistantHTMLSecurityTests
  fi
  if [ "$LANE" = ios18 ]; then
    about_visual() {
      local name="$1" suite="$2" device="$3"
      mkdir -p "$OUTPUT/$name-shots"
      test_batch "$name" "iOS 18.6 $suite" 600 env "TEST_RUNNER_PERSONA_SHOTS=$OUTPUT/$name-shots" \
        "${IOS_COMMON[@]}" -destination "platform=iOS Simulator,id=$device" -destination-timeout 120 \
        -testLanguage en -testRegion US "-only-testing:PersonaTests/$suite" \
        -resultBundlePath "$OUTPUT/$name.xcresult" test
      result "$name" "$device" "$suite"
    }
    about_visual ios-visual-compact AboutVersionCompactVisualTests "$SIM"
  fi

  # The same compiled fixture and compatible runtime as #11289, but on a real
  # iPad whose foreground scene has .regular horizontal size class. This is a
  # focused evidence lane within the Apple job, not a hidden platform skip.
  REGULAR_SIM_STARTED=1
  ci_run 'Create stock regular-width About test simulator' 100 bash -c \
    '"$1" --create-only --profile stock --runtime "$4" --device-type "$6" "$2" > "$3" 2> >(tee "$5" >&2)' \
    -- "$SOURCE/scripts/app/sim-boot.sh" "$REGULAR_SIM_NAME" "$OUTPUT/regular-simulator.log" "$IOS_RUNTIME" "$OUTPUT/regular-simulator-boot.log" "$IOS_TABLET"
  REGULAR_SIM=$(simulator_udid "$OUTPUT/regular-simulator.log")
  if [ "$LANE" = ios18 ]; then
    about_visual ios-visual-regular AboutVersionRegularVisualTests "$REGULAR_SIM"
  else
    # Exercise the real test host on the fresh iPad before the regular batch.
    # Keep its strict probe, launch scope and evidence separate from the iPhone.
    python3 "$CI_ROOT/scripts/ci/apple_launch_diagnostics.py" \
      --record "$OUTPUT/regular-readiness-context.json" --udid "$REGULAR_SIM" --bundle-id "$CI_BUNDLE_ID"
    ci_run 'Verify hosted regular iPad test readiness' 900 bash -c '
      set -euo pipefail
      checker="$1"; bundle="$2"; log="$3"; watch_log="$4"; udid="$5"; ci_root="$6"; bundle_id="$7"; output="$8"; diagnostics="$9"; lane="${10}"; shift 10
      mkdir -p "$watch_log"
      printf "%s\n" "observer incomplete; bounded unavailable final evidence only; no launch cause established" > "$watch_log/observer.status.pending"
      mv "$watch_log/observer.status.pending" "$watch_log/observer.status.txt"
      printf "%s\n" "{\"unavailable\":\"observer did not complete; no host-process verdict\"}" > "$watch_log/host-final.pending"
      mv "$watch_log/host-final.pending" "$watch_log/host-final.json"
      for product in 0 1 2; do
        printf "%s\n" "unavailable: product collection incomplete" > "$watch_log/product-$product-architecture.pending"
        mv "$watch_log/product-$product-architecture.pending" "$watch_log/product-$product-architecture.txt"
        printf "%s\n" "unavailable: product collection incomplete" > "$watch_log/product-$product-libraries.pending"
        mv "$watch_log/product-$product-libraries.pending" "$watch_log/product-$product-libraries.txt"
      done
      for product in 0 1; do
        printf "%s\n" "unavailable: bundle signature collection incomplete" > "$watch_log/product-$product-bundle-signature.pending"
        mv "$watch_log/product-$product-bundle-signature.pending" "$watch_log/product-$product-bundle-signature.txt"
      done
      # Noninteractive Bash gives the observer a separate process group only
      # with job control enabled. The bounded readiness runner must never kill
      # this group while it finishes its bounded final evidence.
      set -m
      bash -c "exec python3 \"\$@\"" -- "$ci_root/scripts/ci/apple_readiness_watch.py" \
        --udid "$udid" --bundle-id "$bundle_id" --scope-root "$output" \
        --app "$output/ios-tests/Build/Products/Debug-iphonesimulator/Persona.app/Persona" \
        --test-bundle "$output/ios-tests/Build/Products/Debug-iphonesimulator/Persona.app/PlugIns/PersonaTests.xctest/PersonaTests" \
        --output "$watch_log" --seconds 900 &
      watcher=$!
      set +m
      stop_watcher() {
        local code=$?
        trap - EXIT TERM INT
        for _ in $(seq 1 50); do
          [ -e "$watch_log/product-ready" ] && break
          kill -0 "$watcher" 2>/dev/null || break
          sleep 0.1
        done
        : > "$watch_log/observer.stop"
        # Signal the independent observer group explicitly; otherwise the
        # stop-file cannot end its polling after the readiness shell is gone.
        kill -TERM "-$watcher" 2>/dev/null || kill -TERM "$watcher" 2>/dev/null || true
        # Let the stop-aware observer finish bounded final evidence. The
        # existing readiness deadline remains the hard outer bound.
        wait "$watcher" || true
        if kill -0 "$watcher" 2>/dev/null; then
          kill -KILL "-$watcher" 2>/dev/null || kill -KILL "$watcher" 2>/dev/null || true
        fi
        wait "$watcher" || true
        if [ "$lane" = ios-ipad-diagnostic ]; then
          timeout --kill-after=5s 180s python3 "$ci_root/scripts/ci/apple_diagnostics.py" \
            "$output" "$diagnostics" --scope regular || true
        fi
        exit "$code"
      }
      trap stop_watcher EXIT TERM INT
      {
        "$@" -only-testing:PersonaTests/TypeScaleTests -resultBundlePath "$bundle" test-without-building
        python3 "$checker" "$bundle" TypeScaleTests
      } 2>&1 | tee "$log"
    ' -- "$CI_ROOT/scripts/ci/apple_result.py" "$OUTPUT/ios-regular-readiness.xcresult" \
      "$OUTPUT/regular-readiness.log" "$OUTPUT/diagnostics/live-regular-readiness" "$REGULAR_SIM" \
      "$CI_ROOT" "$CI_BUNDLE_ID" "$OUTPUT" "$DIAGNOSTICS" "$LANE" "${IOS_COMMON[@]}" \
      -destination "platform=iOS Simulator,id=$REGULAR_SIM" -destination-timeout 120
    if [ "$LANE" = ios-ipad-diagnostic ]; then
      # A deliberate app-host diagnostic stops after its own strict probe and
      # observer cleanup; it neither runs nor substitutes for the visual suite.
      exit 0
    fi
    mkdir -p "$OUTPUT/regular-shots"
    # Observe the actual visual invocation, not only the successful readiness
    # probe before it. Both stay inside the unchanged 600-second outer step.
    TEST_RUNNER_PERSONA_SHOTS="$OUTPUT/regular-shots" \
      ci_run 'iOS regular-width About visual test on real iPad' 600 bash -c '
        set -euo pipefail
        ci_root="$1"; output="$2"; watch_log="$3"; udid="$4"; bundle_id="$5"; shift 5
        mkdir -p "$watch_log"
        printf "%s\n" "collection incomplete; observation only; no launch cause established" > "$watch_log/observer.status.pending"
        mv "$watch_log/observer.status.pending" "$watch_log/observer.status.txt"
        printf "%s\n" "unavailable: observation incomplete" > "$watch_log/launch-system-after-60s.txt.pending"
        mv "$watch_log/launch-system-after-60s.txt.pending" "$watch_log/launch-system-after-60s.txt"
        printf "%s\n" "incomplete" > "$watch_log/launch-system-after-60s.txt.status.pending"
        mv "$watch_log/launch-system-after-60s.txt.status.pending" "$watch_log/launch-system-after-60s.txt.status.txt"
        # The observer owns its process group; the existing deadline still owns
        # both it and xcodebuild. No boot, shutdown or launch setting changes.
        set -m
        python3 "$ci_root/scripts/ci/apple_launch_diagnostics.py" \
          --record "$output/regular-visual-context.json" --udid "$udid" --bundle-id "$bundle_id"
        python3 "$ci_root/scripts/ci/apple_regular_visual_watch.py" \
          --udid "$udid" --bundle-id "$bundle_id" --scope-root "$output" \
          --output "$watch_log" --seconds 600 &
        watcher=$!
        set +m
        stop_watcher() {
          local code=$?
          trap - EXIT TERM INT
          : > "$watch_log/observer.stop"
          wait "$watcher" || true
          if kill -0 "$watcher" 2>/dev/null; then
            kill -KILL "-$watcher" 2>/dev/null || kill -KILL "$watcher" 2>/dev/null || true
          fi
          wait "$watcher" || true
          exit "$code"
        }
        trap stop_watcher EXIT TERM INT
        command_status=0
        "$@" 2>&1 | tee "$output/ios-about-regular.log" || command_status=$?
        exit "$command_status"
      ' -- "$CI_ROOT" "$OUTPUT" "$OUTPUT/diagnostics/live-regular-visual" "$REGULAR_SIM" "$CI_BUNDLE_ID" \
        "${IOS_COMMON[@]}" -destination "platform=iOS Simulator,id=$REGULAR_SIM" -destination-timeout 120 \
        -only-testing:PersonaTests/AboutVersionRegularVisualTests \
        -resultBundlePath "$OUTPUT/ios-about-regular.xcresult" test-without-building
    result ios-about-regular "$REGULAR_SIM" AboutVersionRegularVisualTests
  fi
  ci_run 'Verify token baseline was not rewritten' 60 cmp "$CI_ROOT/apple/PersonaTests/TokenBaseline.json" "$SOURCE/apple/PersonaTests/TokenBaseline.json"
  exit "$IOS_FAILED"
fi

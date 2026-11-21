"""Keep minimum-Xcode platform preparation ahead of destination validation."""

from pathlib import Path
import os
import json
import plistlib
import re
import shlex
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/ci/apple.sh"
sys.path.insert(0, str(ROOT / "scripts/ci"))
import apple_runtime
import apple_result
import apple_diagnostics


class AppleOfflineUnitSchemeTests(unittest.TestCase):
    def setUp(self):
        project = yaml.safe_load((ROOT / 'apple/project.yml').read_text())
        self.schemes = project['schemes']
        self.script = SCRIPT.read_text()

    def test_offline_scheme_excludes_ui_target_and_preserves_default_scheme(self):
        offline = self.schemes['PersonaOfflineUnitTests']
        self.assertEqual(offline['build']['targets'], {'Persona': 'all', 'PersonaTests': ['test']})
        self.assertEqual(offline['test'], {'targets': ['PersonaTests'], 'language': 'zh-Hans', 'region': 'CN'})
        self.assertNotIn('PersonaUITests', str(offline))
        # The existing unit-only visual scheme demonstrates the same minimal build-target pattern.
        for key in ('build', 'run'):
            self.assertEqual(offline[key], self.schemes['AboutVersionVisualTests'][key])
        default = self.schemes['Persona']
        self.assertEqual(default['build']['targets'], {
            'Persona': 'all', 'PersonaTests': ['test'], 'PersonaUITests': ['test'],
        })
        self.assertEqual(default['test']['targets'], ['PersonaTests', 'PersonaUITests'])
        self.assertEqual((default['test']['language'], default['test']['region']), ('zh-Hans', 'CN'))

    def test_offline_build_and_execution_share_the_unit_only_scheme(self):
        common = self.script.split('  IOS_COMMON=(', 1)[1].split('\n', 2)[:2]
        self.assertEqual(common[0], '"${XCODE[@]}" -scheme PersonaOfflineUnitTests -derivedDataPath "$OUTPUT/ios-tests" \\')
        self.assertNotIn('-scheme Persona ', ''.join(common))
        for marker in ('"${IOS_UNIT_SELECTION[@]}" build-for-testing',
                       '"${IOS_UNIT_SELECTION[@]}" \\\n    -resultBundlePath "$OUTPUT/ios-unit.xcresult" test-without-building',
                       '-only-testing:PersonaTests/TypeScaleTests -resultBundlePath "$bundle" test-without-building',
                       '-only-testing:PersonaTests/AboutVersionRegularVisualTests'):
            self.assertIn(marker, self.script)
        self.assertIn('BUILD_SECONDS=1800', self.script)
        self.assertIn('UNIT_SECONDS=1800', self.script)
        self.assertIn("result ios-unit \"$SIM\" TypeScaleTests IconVocabularyTests TokenRatchetTests StyleSpecTests", self.script)

    def test_local_ui_test_entry_keeps_default_scheme_and_ui_target(self):
        ui_test = (ROOT / 'scripts/app/ui-test.sh').read_text()
        self.assertIn('xcodebuild test -project Persona.xcodeproj -scheme Persona -destination "id=$SIM"', ui_test)
        self.assertIn('-only-testing:PersonaUITests', ui_test)
        self.assertNotIn('PersonaOfflineUnitTests', ui_test)
        self.assertNotIn('ui-test.sh', self.script)


class AppleCIPreparationTests(unittest.TestCase):
    def test_hosted_runner_initializes_the_same_minimum_system_toolchain(self):
        workflow = (ROOT / '.github/workflows/apple-test.yml').read_text()
        selection = 'ci_run \'Select system Xcode 26.0\' 60 sudo xcode-select --switch "$DEVELOPER_DIR"'
        initialization = 'ci_run \'Initialize Xcode 26.0 platform services\' 900 sudo env DEVELOPER_DIR="$DEVELOPER_DIR" xcodebuild -runFirstLaunch'
        self.assertIn('DEVELOPER_DIR: /Applications/Xcode_26.0.1.app/Contents/Developer', workflow)
        self.assertLess(workflow.index(selection), workflow.index(initialization))
        self.assertLess(workflow.index(initialization), workflow.index('run: scripts/ci/apple.sh'))
        self.assertIn('source scripts/ci/common.sh', workflow)
        # System-wide initialization is limited to the disposable hosted runner.
        self.assertNotIn('sudo', SCRIPT.read_text())

    def test_hosted_initialization_order_and_failures_with_fake_system_tools(self):
        workflow = (ROOT / '.github/workflows/apple-test.yml').read_text()
        lines = workflow.split('      - name: Initialize pinned Xcode\n        run: |\n', 1)[1].splitlines()
        body = []
        for line in lines:
            if not line.startswith('          '):
                break
            body.append(line[10:])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tools = {
                'sudo': '#!/bin/bash\nexec "$@"\n',
                'xcode-select': '#!/bin/bash\nprintf "select %s\\n" "$*" >> "$CALL_LOG"\n',
                'xcodebuild': '#!/bin/bash\nprintf "initialize %s %s\\n" "$DEVELOPER_DIR" "$*" >> "$CALL_LOG"\nexit "$INITIALIZE_STATUS"\n',
                # ci_run starts python3. On a Mac that is Xcode's /usr/bin/python3 stub, which asks the Xcode in
                # DEVELOPER_DIR and fails when the pinned one is not installed: run this interpreter instead.
                'python3': '#!/bin/bash\nexec %s "$@"\n' % shlex.quote(sys.executable),
            }
            for name, source in tools.items():
                path = root / name
                path.write_text(source)
                path.chmod(0o755)
            log = root / 'commands.log'
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'],
                       DEVELOPER_DIR='/Applications/Xcode_26.0.1.app/Contents/Developer',
                       CALL_LOG=str(log))
            for status in ('0', '1'):
                with self.subTest(status=status):
                    log.write_text('')
                    env['INITIALIZE_STATUS'] = status
                    result = subprocess.run(['bash', '-c', '\n'.join(body)], cwd=ROOT, env=env,
                                            text=True, capture_output=True, timeout=15)
                    self.assertEqual(result.returncode, int(status), result.stdout + result.stderr)
                    expected = [
                        'select --switch ' + env['DEVELOPER_DIR'],
                        'initialize ' + env['DEVELOPER_DIR'] + ' -runFirstLaunch',
                    ]
                    if status == '0':
                        expected.append('select --print-path')
                    self.assertEqual(log.read_text().splitlines(), expected)
                    self.assertIn('PASS' if status == '0' else 'FAIL', result.stdout)

    def test_matching_runtime_is_prepared_before_device_build(self):
        script = SCRIPT.read_text()
        self.assertLess(
            script.index('Download matching $platform 26.0 simulator runtime'),
            script.index("Unsigned iOS and embedded watch/extensions build"),
        )

    def test_explicit_unsigned_build_lane_preserves_the_real_build_and_skips_devices(self):
        script = SCRIPT.read_text()
        workflow = (ROOT / '.github/workflows/apple-test.yml').read_text()
        self.assertIn('options: [ios, ios18, ios-build, packages, ios-focused, ios-regressions, ios-ipad-diagnostic]', workflow)
        self.assertIn("inputs.lane == 'packages' && 'Native package tests (Xcode 26.0)'", workflow)
        self.assertIn("inputs.lane == 'ios-focused' && 'Focused iOS and About tests (Xcode 26.0)'", workflow)
        self.assertIn("inputs.lane == 'ios-regressions' && 'iOS stack and Live Activity regressions (Xcode 26.0)'", workflow)
        self.assertIn('all|ios|ios18|packages|ios-build|ios-focused|ios-regressions|ios-ipad-diagnostic', script)
        self.assertIn('[ "$LANE" = all ] || [ "$LANE" = ios ] || [ "$LANE" = ios-build ] || [ "$LANE" = ios-focused ] || [ "$LANE" = ios-regressions ] || [ "$LANE" = ios-ipad-diagnostic ]', script)
        self.assertIn('[ "$LANE" != packages ] && [ "$LANE" != ios-build ]', script)
        start = script.index('if [ "$LANE" = all ] || [ "$LANE" = ios ] || [ "$LANE" = ios-build ] || [ "$LANE" = ios-focused ] || [ "$LANE" = ios-regressions ]')
        end = script.index('if [ "$LANE" = ios18 ]; then', start)
        device = script[start:end]
        for step in ('Stage resolved packages for Meta configuration',
                     'Build PersonaCore XCFramework before package resolution',
                     'Generate public unsigned Apple config',
                     'Generate Apple project'):
            self.assertLess(script.index(step), start)
        for expected in ('resolve Persona', "-destination 'generic/platform=iOS'",
                         '-disableAutomaticPackageResolution "${UNSIGNED[@]}" build',
                         'No CallKit in the device build', 'Debug view stack frames (device build)',
                         'Download matching $platform 26.0 simulator runtime'):
            self.assertIn(expected, device)
        self.assertNotIn('sim-boot.sh', device)
        self.assertNotIn('PersonaSSH native tunnel integration tests', script[start:])
        self.assertNotIn('Blocking calls on the main actor (isolation lint)', script[start:])

    def test_focused_lane_keeps_real_simulators_and_strict_about_results(self):
        script = SCRIPT.read_text()
        self.assertIn('if [ "$LANE" = ios-focused ] || [ "$LANE" = ios-ipad-diagnostic ]; then', script)
        for suite in ('PageSearchableTests', 'StarredMessagesTests', 'LauncherModelChoiceTests',
                      'OmpNewAgentTests', 'PaseoPermissionModesTests', 'PaseoProvidersTests',
                      'AboutVersionCompactVisualTests'):
            self.assertIn(f'-only-testing:PersonaTests/{suite}', script)
            self.assertIn(suite, script[script.index('result ios-unit "$SIM" PageSearchableTests'):])
        self.assertIn('result ios-unit "$SIM" TypeScaleTests IconVocabularyTests TokenRatchetTests StyleSpecTests', script)
        self.assertNotIn('result ios-unit "$SIM" TypeScaleTests\n  if [ "$LANE" = ios-focused ]; then', script)
        self.assertIn('if [ "$LANE" != ios-focused ]; then', script)
        self.assertIn('result ios-about-regular "$REGULAR_SIM" AboutVersionRegularVisualTests', script)
        self.assertIn('TEST_RUNNER_PERSONA_SHOTS="$OUTPUT/shots"', script)
        self.assertIn('TEST_RUNNER_PERSONA_SHOTS="$OUTPUT/regular-shots"', script)
        self.assertIn('Create stock regular-width About test simulator', script)
        self.assertIn('Verify hosted iOS test readiness', script)

    def test_regular_ipad_diagnostic_is_explicit_and_does_not_change_ordinary_lanes(self):
        workflow = (ROOT / '.github/workflows/apple-test.yml').read_text()
        data = yaml.safe_load(workflow)
        choices = data[True]['workflow_dispatch']['inputs']['lane']['options']
        self.assertIn('ios-ipad-diagnostic', choices)
        ordinary = yaml.safe_load(subprocess.run(
            ['git', 'show', 'af32ff9d9459e4faafbba63cd93b3901bd487534:.github/workflows/apple-test.yml'], cwd=ROOT,
            check=True, capture_output=True, text=True).stdout)
        for lane in ('ios', 'ios-focused', 'ios-regressions'):
            current = dict(data['jobs']['apple'])
            current.pop('steps')
            baseline = dict(ordinary['jobs']['apple'])
            baseline.pop('steps')
            for item in (current, baseline):
                item.pop('name')
            self.assertEqual(current, baseline)
        steps = data['jobs']['apple']['steps']
        upload = next(step for step in steps if step.get('name') == 'Upload regular iPad diagnostic evidence')
        self.assertEqual(upload['if'], "always() && inputs.lane == 'ios-ipad-diagnostic'")
        self.assertEqual(upload['with']['if-no-files-found'], 'error')
        original_steps = ordinary['jobs']['apple']['steps']
        for step in original_steps:
            if step.get('name') == 'Upload Apple failure diagnostics':
                step = dict(step, **{'if': "failure() && inputs.lane != 'ios-ipad-diagnostic'"})
            self.assertIn(step, steps)
        script = SCRIPT.read_text()
        self.assertIn('if [ "$LANE" = ios-ipad-diagnostic ]; then\n      # A deliberate app-host diagnostic stops', script)
        self.assertIn('if [ "$lane" = ios-ipad-diagnostic ]; then\n          timeout --kill-after=5s 180s python3 "$ci_root/scripts/ci/apple_diagnostics.py"', script)
        self.assertIn('"$OUTPUT" "$DIAGNOSTICS" "$LANE" "${IOS_COMMON[@]}"', script)
        self.assertIn("ci_run 'Verify hosted regular iPad test readiness' 900", script)
        self.assertIn("ci_run 'iOS regular-width About visual test on real iPad' 600", script)
        self.assertLess(script.index('exit 0\n    fi\n    mkdir -p "$OUTPUT/regular-shots"'),
                        script.index("ci_run 'iOS regular-width About visual test on real iPad'"))
        self.assertNotIn("ci_run 'Collect regular iPad diagnostic evidence'", script)
        original_script = subprocess.run(['git', 'show', 'af32ff9d9459e4faafbba63cd93b3901bd487534:scripts/ci/apple.sh'], cwd=ROOT,
                                         check=True, capture_output=True, text=True).stdout
        # The packages lane's PersonaWire tests came later (#12734) and are pinned on their own.
        wire = script[script.index('  # The wire layer every app target links,'):script.index("  ci_run 'PersonaKit unit tests'")]
        self.assertIn("ci_run 'PersonaWire unit tests'", wire)
        for lane in ('ios', 'ios-focused', 'ios-regressions'):
            current = script.replace(wire, '')
            current = current.replace(' || [ "$LANE" = ios-ipad-diagnostic ]', '').replace('|ios-ipad-diagnostic', '')
            current = current.replace('      checker="$1"; bundle="$2"; log="$3"; watch_log="$4"; udid="$5"; ci_root="$6"; bundle_id="$7"; output="$8"; diagnostics="$9"; lane="${10}"; shift 10',
                                      '      checker="$1"; bundle="$2"; log="$3"; watch_log="$4"; udid="$5"; ci_root="$6"; bundle_id="$7"; output="$8"; shift 8')
            current = current.replace('      "$CI_ROOT" "$CI_BUNDLE_ID" "$OUTPUT" "$DIAGNOSTICS" "$LANE" "${IOS_COMMON[@]}" \\',
                                      '      "$CI_ROOT" "$CI_BUNDLE_ID" "$OUTPUT" "${IOS_COMMON[@]}" \\')
            current = current.replace('        if [ "$lane" = ios-ipad-diagnostic ]; then\n          timeout --kill-after=5s 180s python3 "$ci_root/scripts/ci/apple_diagnostics.py" \\\n            "$output" "$diagnostics" --scope regular || true\n        fi\n', '')
            current = current.replace('    local scope=all\n    if [ "$LANE" = ios-ipad-diagnostic ]; then\n      scope=regular\n    fi\n', '')
            current = current.replace('      "$OUTPUT" "$DIAGNOSTICS" --scope "$scope"', '      "$OUTPUT" "$DIAGNOSTICS"')
            current = current.replace('    if [ "$LANE" = ios-ipad-diagnostic ]; then\n      # A deliberate app-host diagnostic stops after its own strict probe and\n      # observer cleanup; it neither runs nor substitutes for the visual suite.\n      exit 0\n    fi\n', '')
            self.assertEqual(current, original_script)

    def test_regression_lane_stops_after_two_strict_suites(self):
        script = SCRIPT.read_text()
        selection = script[script.index('if [ "$LANE" = ios-regressions ]; then'):script.index('# A generic destination')]
        self.assertEqual(re.findall(r'-only-testing:PersonaTests/(\w+)', selection), [
            'MainPagesStackTests', 'PhoneSystemToolsTests',
        ])
        self.assertNotIn('IOS_EXCLUSIONS', selection)
        readiness = script.index("ci_run 'Verify hosted iOS test readiness' 900")
        batch = script.index("test_batch ios-unit 'iOS offline unit and design/token tests'")
        strict = script.index('result ios-unit "$SIM" MainPagesStackTests PhoneSystemToolsTests')
        stop = script.rindex('if [ "$LANE" = ios-regressions ]; then', batch, strict)
        finish = script.index('if [ "$LANE" != ios-focused ]; then', strict)
        ordinary = script.index('result ios-unit "$SIM" TypeScaleTests IconVocabularyTests TokenRatchetTests StyleSpecTests')
        focused_start = script.index('if [ "$LANE" = ios-focused ] || [ "$LANE" = ios-ipad-diagnostic ]; then', batch)
        focused_result = script.index('result ios-unit "$SIM" PageSearchableTests', focused_start)
        focused = script[focused_start:script.index('\n', focused_result) + 1]
        self.assertLess(readiness, batch)
        self.assertLess(batch, ordinary)
        self.assertLess(ordinary, stop)
        self.assertLess(stop, strict)
        self.assertEqual(script[stop:finish], '''if [ "$LANE" = ios-regressions ]; then
    result ios-unit "$SIM" MainPagesStackTests PhoneSystemToolsTests
    exit "$IOS_FAILED"
  fi
  ''')
        self.assertLess(strict, script.index("test_batch ios-html-card 'iOS HTML card tests"))
        # The established compact/About branch remains exactly unchanged.
        self.assertEqual(focused, '''if [ "$LANE" = ios-focused ] || [ "$LANE" = ios-ipad-diagnostic ]; then
    result ios-unit "$SIM" PageSearchableTests StarredMessagesTests LauncherModelChoiceTests OmpNewAgentTests PaseoPermissionModesTests PaseoProvidersTests AboutVersionCompactVisualTests
''')
        self.assertIn('elif [ "$LANE" != ios-regressions ]; then', script[focused_start:ordinary])
        self.assertIn('if [ "$LANE" != ios-focused ]; then', script[finish:])

    def test_regular_ipad_observer_has_an_independent_scope_and_strict_probe(self):
        script = SCRIPT.read_text()
        regular = script[script.index('  REGULAR_SIM=$(simulator_udid'):script.index("  ci_run 'Verify token baseline")]
        self.assertIn('--record "$OUTPUT/regular-readiness-context.json" --udid "$REGULAR_SIM" --bundle-id "$CI_BUNDLE_ID"', regular)
        self.assertIn('"$OUTPUT/diagnostics/live-regular-readiness" "$REGULAR_SIM"', regular)
        self.assertNotIn('app_host_readiness', script[script.index("test_batch() {"):script.index('if [ "$LANE" = all ]')])
        self.assertNotIn('app_host_readiness', regular)
        self.assertIn('bash -c "exec python3 \\"\\$@\\"" -- "$ci_root/scripts/ci/apple_readiness_watch.py"', regular)
        self.assertIn('python3 "$checker" "$bundle" TypeScaleTests', regular)
        self.assertIn("-only-testing:PersonaTests/TypeScaleTests", regular)
        self.assertIn("ci_run 'Verify hosted regular iPad test readiness' 900", regular)
        self.assertIn("ci_run 'iOS regular-width About visual test on real iPad' 600", regular)
        self.assertIn('result ios-about-regular "$REGULAR_SIM" AboutVersionRegularVisualTests', regular)
        readiness = regular
        self.assertIn('--app "$output/ios-tests/Build/Products/Debug-iphonesimulator/Persona.app/Persona"', readiness)
        self.assertIn('--test-bundle "$output/ios-tests/Build/Products/Debug-iphonesimulator/Persona.app/PlugIns/PersonaTests.xctest/PersonaTests"', readiness)


    def test_native_ssh_failure_evidence_has_a_timeline_and_a_redacted_server_snapshot(self):
        package = ROOT / 'apple/Packages/PersonaSSH'
        diagnostics = (package / 'Sources/PersonaSSH/Diagnostics.swift').read_text()
        tunnel = (package / 'Sources/PersonaSSH/SSHTunnel.swift').read_text()
        handlers = (package / 'Sources/PersonaSSH/Handlers.swift').read_text()
        harness = (package / 'Tests/PersonaSSHTests/Harness.swift').read_text()
        test = (package / 'Tests/PersonaSSHTests/SSHTunnelTests.swift').read_text()
        self.assertIn('includingFractionalSeconds: true', diagnostics)
        self.assertIn('guard let path = ProcessInfo.processInfo.environment["PERSONA_SSH_TRACE"] else { return }', diagnostics)
        self.assertIn('func trace(_ message: @autoclosure () -> String)', diagnostics)
        self.assertIn('guard ProcessInfo.processInfo.environment["PERSONA_SSH_TRACE"] != nil else { return nil }', diagnostics)
        self.assertIn('traceID: "\\(self.traceID) \\(attemptID)"', tunnel)
        self.assertIn('private func dial(_ attemptID: String?)', tunnel)
        connection = tunnel.split('private func connection() -> EventLoopFuture<Channel> {', 1)[1].split('private func dial(', 1)[0]
        self.assertIn('let attemptID = PersonaSSH.traceID("dial")', connection)
        # Brace-scan the real function body instead of merely forbidding the identifier file-wide.
        opening = tunnel.index('private func open(on session: Channel, joining inbound: Channel?) -> EventLoopFuture<Void> {')
        opening = tunnel.index('{', opening)
        depth, end, quoted, escaped = 0, opening, False, False
        for index in range(opening, len(tunnel)):
            char = tunnel[index]
            if quoted:
                if escaped:
                    escaped = False
                elif char == '\\':
                    escaped = True
                elif char == '"':
                    quoted = False
                continue
            if tunnel.startswith('//', index):
                newline = tunnel.find('\n', index)
                if newline < 0:
                    break
                # Keep newlines in the slice; skip this comment's contents.
                tunnel = tunnel[:index] + ' ' * (newline - index) + tunnel[newline:]
                continue
            if char == '"':
                quoted = True
            elif char == '{':
                depth += 1
            elif char == '}':
                depth -= 1
                if depth == 0:
                    end = index + 1
                    break
        self.assertEqual(depth, 0)
        opened = tunnel[opening:end]
        self.assertTrue(opened.endswith('}'))
        self.assertNotIn('attemptID', opened)
        self.assertIn('trace("\\(self.traceID) channel open timed out', opened)
        self.assertIn('result.fail(SSHTunnelError.timedOut)\n            session.close(promise: nil)', opened)
        for event in ('connection requested', 'dial start', 'session authenticated',
                      'close observed', 'current owner cleared', 'channel open timed out'):
            self.assertIn(event, tunnel)
        for event in ('session viability', 'session error', 'session inactive'):
            self.assertIn('trace("\\(traceID) ' + event, handlers)
        forwarding = test.split('@Test func forwardsRequestsToTheHub() async throws {', 1)[1].split('@Test func', 1)[0]
        self.assertLess(forwarding.index('forwarding test begin'), forwarding.index('forwarding health complete'))
        self.assertLess(forwarding.index('forwarding health complete'), forwarding.index('forwarding session count'))
        self.assertIn('if accepted != 1 { sshd.traceFailure() }', forwarding)
        self.assertIn('#expect(accepted == 1, "unexpected SSH sessions; server log: \\(log)")', forwarding)
        self.assertNotIn('repeat', forwarding)
        self.assertIn('let limit = 65_536', harness)
        self.assertIn('if index == 1000 {', harness)
        self.assertIn('let size = redacted.utf8.count', harness)
        self.assertIn('if bytes + size > limit {', harness)
        self.assertIn('if truncated { trace("sshd failure | snapshot truncated at 1000 lines or \\(limit) bytes") }', harness)
        self.assertIn('.replacingOccurrences(of: user, with: "<test-user>")', harness)
        self.assertIn('"SHA256:[A-Za-z0-9+/=]+", with: "<key-id>"', harness)
        # No new file or upload path: the existing failure collector retains the same opt-in trace.
        self.assertIn('trace("sshd failure | \\(redacted)")', harness)
        self.assertIn('ci_run \'PersonaSSH native tunnel integration tests\' 900 env PERSONA_SSH_TRACE="$OUTPUT/native-ssh-trace.log"', SCRIPT.read_text())
        self.assertIn('"regular-readiness.log", "native-ssh-trace.log",', (ROOT / 'scripts/ci/apple_diagnostics.py').read_text())

    def test_native_ssh_tests_run_before_the_app_and_fail_the_lane_on_error(self):
        script = SCRIPT.read_text()
        packages = script.split('if [ "$LANE" = all ] || [ "$LANE" = ios ] || [ "$LANE" = packages ]; then\n', 1)[1].split('\nfi', 1)[0]
        commands = [line.strip() for line in packages.splitlines() if "ci_run 'PersonaSSH " in line]
        self.assertEqual(len(commands), 1)
        command = commands[0]
        self.assertIn('900 env PERSONA_SSH_TRACE="$OUTPUT/native-ssh-trace.log" xcrun swift test', command)
        self.assertLess(script.index(command), script.index('Unsigned iOS and embedded watch/extensions build'))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / 'output directory'
            output.mkdir()
            xcrun = root / 'xcrun'
            xcrun.write_text('#!/bin/bash\nprintf "%s\\n" "$@" > "$CALL_LOG"\n'
                             'printf "synthetic SSH trace\\n" > "$PERSONA_SSH_TRACE"\nexit "$SSH_STATUS"\n')
            xcrun.chmod(0o755)
            log = root / 'commands.log'
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'],
                       SOURCE=str(root / 'source snapshot'), SPM=str(root / 'spm cache'),
                       OUTPUT=str(output), CALL_LOG=str(log))
            shell = 'source scripts/ci/common.sh\n' + command + '\nprintf "ssh-lane-continued\\n"\n'
            for status in (0, 42):
                with self.subTest(status=status):
                    env['SSH_STATUS'] = str(status)
                    result = subprocess.run(['bash', '-c', shell], cwd=ROOT, env=env,
                                            text=True, capture_output=True, timeout=15)
                    self.assertEqual(result.returncode, status, result.stdout + result.stderr)
                    # The complete native suite runs from the disposable snapshot, with no filters or skips.
                    self.assertEqual(log.read_text().splitlines(), [
                        'swift', 'test', '--package-path', env['SOURCE'] + '/apple/Packages/PersonaSSH',
                        '--scratch-path', env['SPM'] + '/PersonaSSH',
                    ])
                    self.assertEqual((output / 'native-ssh-trace.log').read_text(), 'synthetic SSH trace\n')
                    self.assertEqual('ssh-lane-continued' in result.stdout, status == 0)
                    self.assertIn('PASS' if status == 0 else 'FAIL', result.stdout)

    def test_persona_wire_tests_run_with_warnings_as_errors(self):
        # #12734: -warnings-as-errors on a swift test line also applies to the package's dependencies, so the wire
        # layer the app targets and later packages build on runs its own tests that way in the packages lane.
        script = SCRIPT.read_text()
        packages = script.split('if [ "$LANE" = all ] || [ "$LANE" = ios ] || [ "$LANE" = packages ]; then\n', 1)[1].split('\nfi', 1)[0]
        commands = [line.strip() for line in packages.splitlines() if "ci_run 'PersonaWire " in line]
        self.assertEqual(commands, [
            "ci_run 'PersonaWire unit tests' 300 xcrun swift test --package-path \"$SOURCE/apple/Packages/PersonaWire\" "
            "--scratch-path \"$SPM/PersonaWire\" -Xswiftc -warnings-as-errors"])
        sources = ROOT / 'apple/Packages/PersonaWire/Sources/PersonaWire'
        importing = sorted(f.name for f in sources.glob('*.swift')
                           if re.search(r'^\s*import\s+PersonaWire\b', f.read_text(encoding='utf-8'), re.M))
        self.assertEqual(importing, [], 'a file inside PersonaWire imports its own module, a warning (an error in CI)')

    def test_existing_integration_exclusions_use_exact_swift_testing_selectors(self):
        script = SCRIPT.read_text()
        block = script.split('IOS_EXCLUSIONS=(', 1)[1].split('\n  )', 1)[0]
        actual = {line.strip().strip('"') for line in block.splitlines() if line.strip()}
        self.assertEqual(actual, {
            '-skip-testing:PersonaTests/OldHubCompatibilityIntegrationTests',
            '-skip-testing:PersonaTests/NotificationServiceTests/opensWhatTheRelayDelivered()',
            '-skip-testing:PersonaTests/VideoFileTests/nativeVideoPlaysSeeksAndCleansUp()',
            '-skip-testing:PersonaTests/VideoFileResourceTests/realHTTPReadsMiddleTailAndRejectsChangedRevision()',
        })
        self.assertIn('-skip-testing:PersonaTests/AssistantHTMLSecurityTests/aRealInfiniteLoopCanBeDiscardedWithoutBlockingANewCard()', script)

    def test_cold_ios_build_does_not_consume_readiness_or_execution_deadlines(self):
        script = SCRIPT.read_text()
        build = script.index("ci_run 'Build iOS offline unit tests' ")
        create = script.index('ci_run "Create stock iOS $IOS_VERSION test simulator" 100')
        readiness = script.index("ci_run 'Verify hosted iOS test readiness' 900")
        execution = script.index("test_batch ios-unit 'iOS offline unit and design/token tests'")
        self.assertIn('BUILD_SECONDS=1800', script)
        self.assertIn('UNIT_SECONDS=1800', script)
        self.assertLess(build, create)
        self.assertLess(create, readiness)
        self.assertLess(readiness, execution)
        self.assertIn("-destination 'generic/platform=iOS Simulator'", script[build:create])
        self.assertIn('"${IOS_UNIT_SELECTION[@]}" build-for-testing', script[build:create])
        self.assertIn('"${IOS_UNIT_SELECTION[@]}"', script[execution:])
        self.assertIn('IOS_TESTS=("${IOS_COMMON[@]}"', script)

    def test_callkit_is_checked_before_any_build_and_in_both_built_apps(self):
        # #9290: the source guard in every lane before anything compiles; the binary check on each app right after
        # its build, the device one before the simulator build, the simulator one before a simulator is created.
        script = SCRIPT.read_text()
        source = script.index(
            "ci_run 'No CallKit in Apple sources and build settings' 120 python3 \"$CI_ROOT/scripts/test/test_apple_no_callkit.py\"")
        self.assertLess(script.index('trap cleanup EXIT'), source)
        self.assertLess(source, script.index("ci_run 'Build PersonaCore XCFramework before package resolution'"))
        self.assertLess(source, script.index('if [ "$LANE" = all ] || [ "$LANE" = ios ] || [ "$LANE" = packages ]'))
        device_build = script.index("ci_run 'Unsigned iOS and embedded watch/extensions build' 1800")
        device_check = script.index("ci_run 'No CallKit in the device build' 300")
        simulator_build = script.index("ci_run 'Build iOS offline unit tests' ")
        simulator_check = script.index("ci_run 'No CallKit in the simulator build' 300")
        create = script.index('ci_run "Create stock iOS $IOS_VERSION test simulator" 100')
        self.assertLess(device_build, device_check)
        self.assertLess(device_check, simulator_build)
        self.assertLess(simulator_build, simulator_check)
        self.assertLess(simulator_check, create)
        # Each check reads the products of the build before it.
        self.assertIn('-derivedDataPath "$OUTPUT/ios-device"', script[device_build:device_check])
        self.assertIn('python3 "$CI_ROOT/scripts/release/check_no_callkit.py" \\\n'
                      '    "$OUTPUT/ios-device/Build/Products/Debug-iphoneos/Persona.app"', script[device_check:simulator_build])
        self.assertIn('IOS_COMMON=("${XCODE[@]}" -scheme PersonaOfflineUnitTests -derivedDataPath "$OUTPUT/ios-tests"', script)
        self.assertIn('python3 "$CI_ROOT/scripts/release/check_no_callkit.py" \\\n'
                      '    "$OUTPUT/ios-tests/Build/Products/Debug-iphonesimulator/Persona.app"', script[simulator_check:create])
        guards = (ROOT / 'scripts/ci/guards.sh').read_text()
        self.assertIn("ci_run 'No CallKit in Apple sources and build settings' 120 python3 scripts/test/test_apple_no_callkit.py",
                      guards)

    def test_native_mac_lane_is_removed_and_ios_excludes_the_old_hub_lab(self):
        script = SCRIPT.read_text()
        self.assertNotIn('macOS offline unit tests', script)
        self.assertNotIn('PersonaMacTests', script)
        self.assertIn('-skip-testing:PersonaTests/OldHubCompatibilityIntegrationTests', script)

    def test_reminder_round_trip_is_enabled_with_owned_simulator_permission(self):
        script = SCRIPT.read_text()
        self.assertIn('xcrun simctl privacy "$2" grant reminders "$3"', script)
        self.assertIn('"$1" --start-only --profile stock "$2"', script)
        # One attempt, with room for a slow first grant on a fresh stock simulator (#1402), never a retry loop.
        self.assertIn("ci_run 'Grant Reminders access to the owned test app' 300 bash -c", script)
        self.assertEqual(script.count('grant reminders'), 1)
        self.assertIn('export TEST_RUNNER_PERSONA_EVENTKIT_ROUNDTRIP=1', script)
        self.assertLess(script.index('Grant Reminders access'), script.index('iOS offline unit and design/token tests'))
        self.assertNotIn('skip-testing:PersonaTests/PlaceReminderTests', script)

    def test_floor_runs_both_visual_suites_on_their_real_size_class(self):
        script = SCRIPT.read_text()
        for size in ("Compact", "Regular"):
            suite = f"AboutVersion{size}VisualTests"
            self.assertIn("-skip-testing:PersonaTests/" + suite, script)
            self.assertIn(f"about_visual ios-visual-{size.lower()} {suite}", script)
        self.assertIn('IOS_TABLET="iPad (10th generation)"', script)
        tablet = script.index('REGULAR_SIM=$(simulator_udid')
        self.assertLess(script.index("about_visual ios-visual-compact"), tablet)
        self.assertGreater(script.index("about_visual ios-visual-regular"), tablet)
        self.assertIn("TEST_RUNNER_PERSONA_SHOTS=$OUTPUT/$name-shots", script)
        self.assertIn('-testLanguage en -testRegion US', script)
        self.assertIn('about_visual ios-visual-compact AboutVersionCompactVisualTests "$SIM"', script)
        self.assertIn('about_visual ios-visual-regular AboutVersionRegularVisualTests "$REGULAR_SIM"', script)
        self.assertIn('result "$name" "$device" "$suite"', script)

    def test_native_mac_profile_preparation_is_gone_and_ios_tests_use_ad_hoc_signing(self):
        script = SCRIPT.read_text()
        self.assertNotIn('Prepare profile-free Mac offline test entitlements', script)
        self.assertNotIn('macOS offline unit tests', script)
        self.assertNotIn('PersonaMac.entitlements', script)
        self.assertNotIn('PersonaMacNotify.entitlements', script)
        self.assertIn('TEST_SIGNING=(CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=-', script)
        self.assertNotIn('ENABLE_HARDENED_RUNTIME=NO', script)

    def test_runtime_remains_pinned_to_minimum_compiler(self):
        script = SCRIPT.read_text()
        self.assertIn('for platform in iOS watchOS; do', script)
        self.assertIn('xcodebuild -downloadPlatform "$platform" -architectureVariant universal', script)
        self.assertIn('--platform "$platform"', script)
        self.assertNotIn("-buildVersion 26.0", script)
        self.assertIn('--profile stock --runtime "$4"', script)
        self.assertIn('"$OUTPUT/simulator.log" "$IOS_RUNTIME" "$OUTPUT/simulator-boot.log" "$IOS_PHONE"', script)
        self.assertIn('"$OUTPUT/regular-simulator.log" "$IOS_RUNTIME" "$OUTPUT/regular-simulator-boot.log" "$IOS_TABLET"', script)
        self.assertIn("FAIL select Xcode 26.0.x explicitly", script)


class AppleFloorResultsTests(unittest.TestCase):
    suite_start = "Test Suite 'All tests' started at 2026-01-01."
    marker = "Restarting after unexpected exit, crash, or test timeout"
    suite = "PersonaCoreContinuityTests"
    known = "testEveryContinuityFixtureAgainstNativeChatStore"

    def verify(self, *lines):
        apple_result.verify_restarts([self.suite_start, *lines])

    def test_clean_and_two_exact_continuity_restarts(self):
        self.verify()
        for case in apple_result.KNOWN_RESTARTS:
            cls, method = case.split("/")
            self.verify(f"{self.marker} in -[PersonaTests.{cls} {method}]; summary will include totals")
        self.verify(f"Test Case '-[PersonaTests.{self.suite} {self.known}]' started.", self.marker)

    def test_new_unattributed_repeated_and_unreadable_restarts_fail(self):
        started = f"Test Case '-[PersonaTests.{self.suite} {self.known}]' started."
        ended = started.replace("started", "passed")
        known = f"{self.marker} in PersonaTests.{self.suite}.{self.known}(); summary will include totals"
        for lines in ([self.marker], [started, ended, self.marker], [started, "◇ Test run started.", self.marker], [known, known],
                      [started, f"{self.marker} in -[PersonaTests.VideoAttachTests export]"],
                      [f"{self.marker} in -[PersonaTests.{self.suite} testNewCrash]"],
                      [started, f"{self.marker} in unidentified runner"]):
            with self.subTest(lines=lines), self.assertRaisesRegex(ValueError, "unexpected test-host"):
                self.verify(*lines)
        with self.assertRaisesRegex(ValueError, "missing XCTest"):
            apple_result.verify_restarts(["xcodebuild finished"])

    def test_xcresult_destination_and_assertions_are_independent_of_known_restarts(self):
        device = dict(platform="iOS Simulator", osVersion="18.6", deviceId="phone",
                      modelName="iPhone 16 Pro", osBuildNumber="22G86")
        summary = dict(devicesAndConfigurations=[dict(device=device)], result="Passed",
                       failedTests=0, skippedTests=0, testFailures=[])

        def run(_command, **_kwargs):
            return subprocess.CompletedProcess([], 0, json.dumps(summary), "")

        apple_result.verify_destination("test.xcresult", "phone", run)
        with self.assertRaisesRegex(ValueError, "requested iOS 18.6"):
            apple_result.verify_destination("test.xcresult", "other-phone", run)
        for field, value in (("osVersion", "26.3"), ("platform", "iOS")):
            with self.subTest(field=field), patch.dict(device, {field: value}), self.assertRaises(ValueError):
                apple_result.verify_destination("test.xcresult", "phone", run)
        with patch.dict(summary, result="Failed", failedTests=1, testFailures=[{"testName": self.known}]), \
                self.assertRaisesRegex(ValueError, "1 failed"):
            apple_result.verify_destination("test.xcresult", "phone", run)

    def test_exit_65_is_allowed_only_for_the_observed_allocator_abort(self):
        case = f"{self.suite}/{self.known}()"
        started = f"Test Case '-[PersonaTests.{self.suite} {self.known}]' started."
        allocator = "Persona(pid,tid) malloc: *** error for object ptr: pointer being freed was not allocated"
        known = apple_result.verify_restarts([self.suite_start, started, allocator, self.marker])
        self.assertEqual(known, {case})
        self.assertFalse(apple_result.verify_restarts([self.suite_start, started, self.marker]))
        device = dict(platform="iOS Simulator", osVersion="18.6", deviceId="phone")
        failure = dict(targetName="PersonaTests", testIdentifierString=case, failureText="Test crashed with signal abrt.")
        summary = dict(devicesAndConfigurations=[dict(device=device)], result="Failed",
                       failedTests=1, skippedTests=0, testFailures=[failure])

        def check(status=65, known_aborts=known):
            def run(_command, **_kwargs):
                return subprocess.CompletedProcess([], 0, json.dumps(summary), "")
            return apple_result.verify_destination("test.xcresult", "phone", run,
                                                  known_aborts=known_aborts, xcode_status=status)

        self.assertEqual(check(), {case})
        with self.assertRaises(ValueError):
            check(124)
        with self.assertRaises(ValueError):
            check(known_aborts=set())
        for changes in (dict(failureText="XCTAssertEqual failed"), dict(targetName="AnotherTarget"),
                        dict(testIdentifierString="AnotherSuite/testNewCrash()")):
            with self.subTest(changes=changes), patch.dict(failure, changes), self.assertRaises(ValueError):
                check()
        assertion = dict(failure, failureText="XCTAssertEqual failed")
        with patch.dict(summary, testFailures=[failure, assertion]), self.assertRaises(ValueError):
            check()
        with patch.dict(summary, skippedTests=1), self.assertRaises(ValueError):
            check()
        with patch.dict(summary, result="Passed", failedTests=0, testFailures=[]), self.assertRaises(ValueError):
            check()



class AppleFailureDiagnosticsTests(unittest.TestCase):
    def test_only_diagnostics_leave_output_and_result_symlinks_are_not_followed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output, destination = root / 'output', root / 'artifacts'
            output.mkdir()
            for name in ('simulator.log', 'simulator-boot.log', 'simulator-readiness.log', 'runtimes-iOS-before.json',
                         'regular-simulator.log', 'regular-simulator-boot.log', 'regular-readiness.log', 'native-ssh-trace.log'):
                (output / name).write_text('diagnostic fixture')
            (output / 'config.json').write_text('configuration must stay in output')
            (output / 'private.key').write_text('key fixture must stay in output')
            (output / 'native-ssh-trace-private.log').write_text('unlisted trace must stay in output')
            result = output / 'ios-unit.xcresult'
            result.mkdir()
            (result / 'summary.json').write_text('test results')
            (result / 'external-key').symlink_to(output / 'private.key')
            for name in ("live-readiness", "live-regular-readiness"):
                observer = output / "diagnostics" / name
                observer.mkdir(parents=True)
                (observer / "observer.status.txt").write_text(name)
                (observer / "private.key").write_text("must stay in output")
                (observer / "external.json").symlink_to(output / "private.key")
            calls = []
            def runner(command, **kwargs):
                calls.append((command, kwargs))
                if command[0] == 'df':
                    return subprocess.CompletedProcess(command, 0, 'disk space diagnostic', '')
                return subprocess.CompletedProcess(command, 0, '{"devices":{}}', '')
            apple_diagnostics.collect(output, destination, runner)
            self.assertEqual(calls[0][0], ['xcrun', 'simctl', 'list', '--json'])
            self.assertEqual(calls[0][1]['timeout'], 120)
            self.assertEqual(calls[1][0], ['df', '-h', str(output)])
            self.assertEqual(calls[1][1]['timeout'], 30)
            self.assertEqual((destination / 'disk-space.txt').read_text(), 'disk space diagnostic')
            self.assertEqual({path.name for path in destination.iterdir()}, {
                'simulator.log', 'simulator-boot.log', 'simulator-readiness.log', 'runtimes-iOS-before.json',
                'regular-simulator.log', 'regular-simulator-boot.log', 'regular-readiness.log', 'native-ssh-trace.log',
                'ios-unit.xcresult', 'simctl-list.json', 'simctl-list.stderr.txt', 'simctl-list.status.txt',
                'live-readiness', 'live-regular-readiness',
                'disk-space.txt', 'disk-space.stderr.txt', 'disk-space.status.txt',
                'simulator-launch.status.txt',
            })
            self.assertEqual((destination / 'ios-unit.xcresult/summary.json').read_text(), 'test results')
            self.assertEqual((destination / 'native-ssh-trace.log').read_text(), 'diagnostic fixture')
            self.assertFalse((destination / 'ios-unit.xcresult/external-key').exists())

    def test_regular_diagnostic_scope_excludes_other_suite_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / 'output'
            destination = root / 'artifacts'
            for name in ('regular-simulator.log', 'regular-simulator-boot.log', 'regular-readiness.log',
                         'simulator.log', 'simulator-readiness.log', 'native-ssh-trace.log'):
                output.mkdir(exist_ok=True)
                (output / name).write_text(name)
            for name in ('ios-regular-readiness.xcresult', 'ios-readiness.xcresult', 'ios-unit.xcresult'):
                (output / name).mkdir()
                (output / name / 'tests.json').write_text(name)
            for name in ('live-readiness', 'live-regular-readiness'):
                observer = output / 'diagnostics' / name
                observer.mkdir(parents=True)
                (observer / 'observer.status.txt').write_text(name)
            (output / 'regular-readiness-context.json').write_text('{"scope":"regular"}')
            (output / 'simulator-readiness-context.json').write_text('phone scope')
            (output / 'simulator.log').write_text('phone-device')
            apple_diagnostics.collect(output, destination, lambda command, **kwargs: subprocess.CompletedProcess(command, 0, '', ''), scope='regular')
            self.assertEqual({path.name for path in destination.iterdir()}, {
                'regular-simulator.log', 'regular-simulator-boot.log', 'regular-readiness.log',
                'ios-regular-readiness.xcresult', 'regular-readiness-context.json', 'live-regular-readiness',
                'simctl-list.json', 'simctl-list.stderr.txt', 'simctl-list.status.txt',
                'disk-space.txt', 'disk-space.stderr.txt', 'disk-space.status.txt',
            })
            self.assertEqual((destination / 'live-regular-readiness/observer.status.txt').read_text(), 'live-regular-readiness')
            self.assertEqual(json.loads((destination / 'regular-readiness-context.json').read_text()), {'scope': 'regular'})

    def test_regular_diagnostic_scope_is_deterministic_before_context_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'output'
            output.mkdir()
            (output / 'regular-readiness.log').write_text('probe failed before context')
            destination = Path(directory) / 'artifacts'
            apple_diagnostics.collect(output, destination, lambda command, **kwargs: subprocess.CompletedProcess(command, 0, '', ''), scope='regular')
            context = json.loads((destination / 'regular-readiness-context.json').read_text())
            self.assertEqual(context, {'unavailable': 'regular iPad probe did not publish an owned regular-file context'})
            self.assertFalse((destination / 'simulator-launch.status.txt').exists())
            self.assertTrue((destination / 'regular-readiness.log').is_file())

    def test_regular_context_symlink_is_rejected_without_reading_its_target(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'output'
            output.mkdir()
            (output / 'private.json').write_text('private target must not be exported')
            (output / 'regular-readiness-context.json').symlink_to(output / 'private.json')
            destination = Path(directory) / 'artifacts'
            apple_diagnostics.collect(output, destination, lambda command, **kwargs: subprocess.CompletedProcess(command, 0, '', ''), scope='regular')
            context = json.loads((destination / 'regular-readiness-context.json').read_text())
            self.assertEqual(context, {'unavailable': 'regular iPad probe did not publish an owned regular-file context'})
            self.assertNotIn('private target', (destination / 'regular-readiness-context.json').read_text())
            self.assertFalse((destination / 'regular-readiness-context.json').is_symlink())
            for name, contents in (('oversize', 'x' * 1025), ('invalid', 'not-json')):
                with self.subTest(case=name):
                    (output / 'regular-readiness-context.json').unlink()
                    (output / 'regular-readiness-context.json').write_text(contents)
                    apple_diagnostics.collect(output, destination, lambda command, **kwargs: subprocess.CompletedProcess(command, 0, '', ''), scope='regular')
                    self.assertEqual(json.loads((destination / 'regular-readiness-context.json').read_text()), context)

    def test_unavailable_simctl_still_retains_existing_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'output'
            output.mkdir()
            (output / 'simulator-boot.log').write_text('boot failed')
            destination = Path(directory) / 'artifacts'
            def unavailable(*args, **kwargs):
                raise OSError('simctl unavailable')
            apple_diagnostics.collect(output, destination, unavailable)
            self.assertEqual((destination / 'simulator-boot.log').read_text(), 'boot failed')
            self.assertEqual((destination / 'simctl-list.status.txt').read_text(), 'unavailable\n')

    def test_upload_is_failure_only_and_boot_limits_and_cleanup_remain_strict(self):
        script = SCRIPT.read_text()
        workflow = (ROOT / '.github/workflows/apple-test.yml').read_text()
        helper = (ROOT / 'scripts/app/sim-boot.sh').read_text()
        self.assertIn('ci_run "Create stock iOS $IOS_VERSION test simulator" 100', script)
        self.assertIn("ci_run 'Verify hosted iOS test readiness' 900", script)
        self.assertIn('--create-only --profile stock', script)
        self.assertIn('2> >(tee "$5" >&2)', script)
        self.assertIn('if [ "$code" -ne 0 ]; then', script)
        cleanup = script[script.index('cleanup() {'):script.index('trap cleanup EXIT')]
        self.assertLess(cleanup.index("ci_run 'Collect Apple failure diagnostics'"), cleanup.index('cleanup_device "$SIM"'))
        cleanup_helper = script[script.index('cleanup_device() {'):script.index('simulator_udid() {')]
        self.assertIn("ci_run 'Shut down owned simulator'", cleanup_helper)
        self.assertIn("ci_run 'Delete owned simulator'", cleanup_helper)
        self.assertEqual(cleanup.count('cleanup_device "$SIM"'), 1)
        self.assertEqual(cleanup.count('cleanup_device "$REGULAR_SIM"'), 1)
        self.assertIn('exit "$code"', script)
        self.assertIn('if: failure()\n        uses: actions/upload-artifact@v4', workflow)
        self.assertIn('CI_APPLE_DIAGNOSTICS: ${{ runner.temp }}/persona-apple-diagnostics-${{ inputs.lane }}-${{ github.run_id }}-${{ github.run_attempt }}', workflow)
        self.assertIn('path: ${{ runner.temp }}/persona-apple-diagnostics-${{ inputs.lane }}-${{ github.run_id }}-${{ github.run_attempt }}', workflow)
        self.assertNotIn('for attempt in', helper)
        self.assertNotIn('restarting once', helper)


class AppleRuntimeTests(unittest.TestCase):
    def runtime(self, version, available=True, name=None, platform="iOS"):
        return {"version": version, "isAvailable": available,
                "name": name or platform + " " + version,
                "identifier": "com.apple.CoreSimulator.SimRuntime." + platform + "-" + version.replace(".", "-")}

    def test_exact_and_patch_versions_are_compatible(self):
        for version in ("26.0", "26.0.1"):
            runtime = self.runtime(version)
            self.assertEqual(apple_runtime.select([runtime]), runtime["identifier"])

    def test_newer_minors_unavailable_beta_and_other_platforms_are_rejected(self):
        for runtime in (self.runtime("26.1"), self.runtime("26.5"),
                        self.runtime("26.0", available=False),
                        self.runtime("26.0", name="iOS 26.0 beta"),
                        self.runtime("26.0", platform="watchOS")):
            with self.subTest(runtime=runtime):
                self.assertIsNone(apple_runtime.select([runtime]))
        self.assertIsNone(apple_runtime.select([]))

    def test_floor_requires_exactly_available_stable_ios_18_6(self):
        exact = self.runtime("18.6")
        others = [self.runtime("18.5"), self.runtime("18.6.1"), self.runtime("26.0"),
                  self.runtime("18.6", False), self.runtime("18.6", name="iOS 18.6 beta"),
                  self.runtime("18.6", platform="watchOS")]
        self.assertIsNone(apple_runtime.select(others, version="18.6"))
        self.assertEqual(apple_runtime.select(others + [exact], version="18.6"), exact["identifier"])

    def test_watch_runtime_is_selected_separately_from_ios(self):
        ios = self.runtime("26.0.1")
        watch = self.runtime("26.0", platform="watchOS")
        self.assertEqual(apple_runtime.select([ios, watch], "watchOS"), watch["identifier"])
        self.assertIsNone(apple_runtime.select([ios], "watchOS"))
        self.assertIsNone(apple_runtime.select([self.runtime("26.2", platform="watchOS")], "watchOS"))

    def test_highest_compatible_patch_wins_without_selecting_newer_minor(self):
        expected = self.runtime("26.0.10")
        self.assertEqual(apple_runtime.select([self.runtime("26.0"), self.runtime("26.0.2"),
                                               self.runtime("26.5"), expected]), expected["identifier"])


class MacOSDeploymentFloorTests(unittest.TestCase):
    AFFECTED_PACKAGES = {
        "BeautifulUI": [".iOS(.v18)", ".visionOS(.v26)"],
        "PersonaCore": [".iOS(.v18)"],
        "PersonaKit": [".iOS(.v18)", ".visionOS(.v26)"],
        "PersonaSSH": [".iOS(.v18)", ".visionOS(.v26)"],
        "PersonaWire": [".iOS(.v18)", ".visionOS(.v26)", ".watchOS(.v10)"],
    }

    def test_every_tracked_swift_package_with_macos_support_has_a_15_or_older_floor(self):
        listed = subprocess.run(["git", "ls-files", "-z"], cwd=ROOT, check=True, capture_output=True,
                                text=True).stdout
        packages = {name for name in listed.split("\0")
                    if name == "Package.swift" or name.endswith("/Package.swift")}
        required = {f"apple/Packages/{name}/Package.swift" for name in self.AFFECTED_PACKAGES}
        self.assertTrue(required <= packages, f"package inventory is incomplete: {sorted(required - packages)}")
        for name in sorted(packages):
            with self.subTest(package=name):
                manifest = (ROOT / name).read_text()
                declarations = re.findall(r"\.macOS\(\s*([^()]+?)\s*\)", manifest)
                if not declarations and re.search(r"\bplatforms\s*:", manifest):
                    continue  # An explicit platform list without macOS does not claim Mac support.
                self.assertEqual(len(declarations), 1, f"{name} must declare one explicit macOS floor")
                match = re.fullmatch(r"\.v(\d+)(?:_\d+)?", declarations[0])
                self.assertIsNotNone(match, f"{name} has an unrecognized macOS floor: {declarations[0]}")
                self.assertLessEqual(int(match.group(1)), 15,
                                     f"{name} must support macOS 15, not {declarations[0]}")

    def test_lowering_macos_floors_preserves_the_other_apple_platform_floors(self):
        for package, expected in self.AFFECTED_PACKAGES.items():
            with self.subTest(package=package):
                manifest = (ROOT / f"apple/Packages/{package}/Package.swift").read_text()
                actual = [f".{platform}({version})" for platform, version in
                          re.findall(r"\.(iOS|visionOS|watchOS|tvOS)\((\.[^)]+)\)", manifest)]
                self.assertCountEqual(actual, expected)


if __name__ == "__main__":
    unittest.main()

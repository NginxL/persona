#!/usr/bin/env python3
"""Install and run an already assembled packaged-JNI test APK with bounded waits and real JUnit evidence."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import uuid
import zipfile


def run(command, timeout=120, **kwargs):
    return subprocess.run(command, timeout=timeout, check=True, text=True, **kwargs)


def parse_junit(output, expected_identities):
    """Require a start and exactly one successful completion for every selected test, not just a final count."""
    current_class = None
    current_test = None
    started = set()
    completed = set()
    for line in output.splitlines():
        if line.startswith('INSTRUMENTATION_STATUS: class='):
            current_class = line.split('=', 1)[1].strip()
        elif line.startswith('INSTRUMENTATION_STATUS: test='):
            current_test = line.split('=', 1)[1].strip()
        elif line.startswith('INSTRUMENTATION_STATUS_CODE:'):
            status = int(line.split(':', 1)[1].strip())
            identity = (current_class, current_test)
            if status == 1:
                started.add(identity)
            elif status == 0:
                if identity in completed:
                    raise ValueError('duplicate JUnit completion: ' + str(identity))
                completed.add(identity)
            else:
                raise ValueError('JUnit test did not complete successfully: ' + str(identity))
    if set(expected_identities) != started or set(expected_identities) != completed:
        raise ValueError('incomplete or unexpected JUnit tests: expected ' + str(sorted(expected_identities))
                         + ', started ' + str(sorted(started)) + ', completed ' + str(sorted(completed)))
    if 'FAILURES!!!' in output or 'INSTRUMENTATION_CODE: 0' in output:
        raise ValueError('instrumentation reported failures or no executed tests')


def fixture_identities(apk):
    """The same file/version/nonempty/unique-name contract as DeviceFixtureReader, with exact test names."""
    names = []
    try:
        with zipfile.ZipFile(apk) as package:
            files = sorted(name for name in package.namelist()
                           if name.startswith('assets/continuity/') and name.endswith('.json'))
            if not files:
                raise ValueError('no packaged continuity fixtures')
            for name in files:
                fixture = json.loads(package.read(name))
                if fixture.get('version') != 1:
                    raise ValueError('unsupported fixture version: ' + name)
                cases = fixture.get('cases')
                if not isinstance(cases, list) or not cases:
                    raise ValueError('empty or missing fixture cases: ' + name)
                names.extend(case['name'] for case in cases)
    except (KeyError, TypeError, json.JSONDecodeError) as error:
        raise ValueError('invalid packaged continuity fixture: ' + str(error)) from error
    if len(names) != len(set(names)):
        raise ValueError('duplicate packaged continuity case names')
    return [('persona.core.rustcore.ContinuityBindingFixtureTest', 'replay[' + name + ']') for name in names]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--serial', required=True, help='an explicitly selected emulator or hardware device')
    parser.add_argument('--suite', choices=('replay', 'benchmark'), default='replay')
    parser.add_argument('--apk', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--timeout', type=int, default=600)
    args = parser.parse_args()
    if args.timeout < 1:
        raise ValueError('--timeout must be positive')
    if not args.apk.is_file():
        raise ValueError('missing assembled test APK: ' + str(args.apk))
    args.output.mkdir(parents=True, exist_ok=True)
    sdk = Path(os.environ['ANDROID_HOME'])
    if args.suite == 'benchmark':
        # Never label a debug APK as optimized just because it contains the benchmark class. The release test
        # has its own R8 mapping and is non-debuggable; both must be true for this evidence to be accepted.
        if '/androidTest/release/' not in str(args.apk):
            raise ValueError('benchmark requires the non-debuggable release androidTest APK')
        mapping = args.apk.parents[3] / 'mapping' / 'releaseAndroidTest' / 'mapping.txt'
        # R8 writes mapping after packaging the test APK, so mapping's mtime is normally later. Rebuild that
        # exact variant before installing rather than accepting an unrelated debug or stale release artifact.
        android = args.apk.parents[5].parents[1]
        repository = android.parent
        result = run(['bash', str(android / 'gradlew'), '--no-daemon', '--no-watch-fs', '--console=plain', '--max-workers=2',
                      '-PpersonaApplicationId=com.example.persona', '-PpersonaGoogleOAuthScheme=persona.oauth.unset',
                      '-PpersonaRepoRoot=' + str(repository),
                      ':core:rustcore-android:minifyReleaseAndroidTestWithR8',
                      ':core:rustcore-android:assembleReleaseAndroidTest'],
                     cwd=android, timeout=args.timeout, capture_output=True)
        (args.output / 'gradle.txt').write_text(result.stdout + result.stderr)
        if not mapping.is_file():
            raise ValueError('benchmark requires the matching releaseAndroidTest R8 mapping')
        tools = sdk / 'build-tools/37.0.0'
        badging = run([str(tools / 'aapt2'), 'dump', 'badging', str(args.apk)],
                      timeout=30, capture_output=True).stdout
        if 'application-debuggable' in badging:
            raise ValueError('benchmark APK must be non-debuggable')
        (args.output / 'r8-mapping.txt').write_text(str(mapping) + '\n')
    adb = [str(sdk / 'platform-tools/adb')]
    if os.environ.get('ANDROID_ADB_SERVER_PORT'):
        adb += ['-P', os.environ['ANDROID_ADB_SERVER_PORT']]
    adb += ['-s', args.serial]
    run(adb + ['wait-for-device'], timeout=min(120, args.timeout))
    props = {}
    for name in ('ro.product.model', 'ro.product.cpu.abi', 'ro.build.version.sdk', 'ro.build.display.id'):
        props[name] = run(adb + ['shell', 'getprop', name], timeout=30, capture_output=True).stdout.strip()
    props['kernelPageBytes'] = run(adb + ['shell', 'getconf', 'PAGE_SIZE'], timeout=30, capture_output=True).stdout.strip()
    (args.output / 'device.txt').write_text('\n'.join(key + '=' + value for key, value in props.items()) + '\n')
    run(adb + ['install', '-r', str(args.apk)], timeout=min(300, args.timeout))
    if args.suite == 'benchmark':
        # Clear this synthetic test package's state, not merely an old report path on /sdcard (whose FUSE
        # transport may be unavailable). No hub, pairing or user data is involved in this test-only package.
        cleared = run(adb + ['shell', 'pm', 'clear', 'persona.core.rustcore.android.test'],
                      timeout=30, capture_output=True).stdout.strip()
        if cleared != 'Success':
            raise ValueError('cannot clear the synthetic benchmark package: ' + cleared)
    test = 'persona.core.rustcore.ContinuityBindingFixtureTest' if args.suite == 'replay' else 'persona.core.rustcore.RustCoreDeviceBenchmark'
    nonce = uuid.uuid4().hex if args.suite == 'benchmark' else ''
    command = adb + ['shell', 'am', 'instrument', '-w', '-r']
    if nonce:
        command += ['-e', 'rustCoreBenchmarkNonce', nonce]
    command += ['-e', 'class', test,
                'persona.core.rustcore.android.test/androidx.test.runner.AndroidJUnitRunner']
    started = time.monotonic()
    try:
        result = run(command, timeout=args.timeout, capture_output=True)
        output = result.stdout + result.stderr
    except subprocess.TimeoutExpired as error:
        (args.output / 'instrumentation.txt').write_text((error.stdout or '') + (error.stderr or ''))
        raise ValueError(f'instrumentation TIMEOUT after {args.timeout}s')
    (args.output / 'instrumentation.txt').write_text(output)
    tests = re.findall(r'INSTRUMENTATION_STATUS: numtests=(\d+)', output)
    counts = {'tests': int(tests[-1]) if tests else 0,
              'failures': len(re.findall(r'INSTRUMENTATION_STATUS_CODE: -2', output)),
              'instrumentationCode': int(re.search(r'INSTRUMENTATION_CODE: (-?\d+)', output).group(1))
              if re.search(r'INSTRUMENTATION_CODE: (-?\d+)', output) else None}
    (args.output / 'counts.json').write_text(json.dumps(counts, indent=2) + '\n')
    expected = 1 if args.suite == 'benchmark' else None
    if args.suite == 'replay':
        identities = fixture_identities(args.apk)
        expected = len(identities)
        (args.output / 'fixture-count.txt').write_text(str(expected) + '\n')
    else:
        identities = [('persona.core.rustcore.RustCoreDeviceBenchmark', 'dispatchAndFrameCosts')]
    if counts != {'tests': expected, 'failures': 0, 'instrumentationCode': -1}:
        raise ValueError('real JUnit result is not a pass: ' + str(counts) + '; see instrumentation.txt')
    parse_junit(output, identities)
    if args.suite == 'benchmark':
        report = run(adb + ['shell', 'cat',
                            '/sdcard/Android/data/persona.core.rustcore.android.test/files/rust-core-benchmark.json'],
                     timeout=30, capture_output=True)
        (args.output / 'benchmark.json').write_text(report.stdout)
        measurements = json.loads(report.stdout)
        if measurements.get('nonce') != nonce:
            raise ValueError('benchmark report does not belong to this run')
        expected_device = {'model': props['ro.product.model'], 'abi': props['ro.product.cpu.abi'],
                           'api': int(props['ro.build.version.sdk']),
                           'build': props['ro.build.display.id'], 'kernelPageBytes': int(props['kernelPageBytes'])}
        for key, expected_value in expected_device.items():
            if measurements.get(key) != expected_value:
                raise ValueError(f'benchmark device mismatch for {key}: {measurements.get(key)!r} != {expected_value!r}')
        if measurements.get('samples') != 1000:
            raise ValueError('benchmark must contain 1000 measured samples')
        for metric in ('dispatchMicros', 'drainDecodeApplyMicros'):
            if not all(isinstance(measurements[metric].get(key), (int, float))
                       for key in ('p50', 'p99', 'max')):
                raise ValueError('benchmark is missing p50/p99/max: ' + metric)
    print('Android core device: PASS (%s; %s; %s; %.1fs)' % (args.serial, args.suite, counts, time.monotonic() - started))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError, subprocess.CalledProcessError) as error:
        sys.exit('Android core device: FAILED (' + str(error) + ')')

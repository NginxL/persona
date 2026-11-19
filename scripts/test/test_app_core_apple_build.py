"""native/app-core/scripts/build-apple.sh: the shared XCFramework cache and the simulator-only build (#10713).

Runs the real script in disposable checkouts with fake cargo, rustup, rustc, xcrun, xcodebuild and uname on PATH, so
it needs no Rust toolchain or Xcode and runs on Linux too. The fake uname makes every host an Apple silicon Mac: the
script refuses any other machine name, and Linux on arm64 says aarch64 where a Mac says arm64.
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'native/app-core/scripts/build-apple.sh'
SIM_TARGET = 'aarch64-apple-ios-sim'
SHASUM = shutil.which('shasum')
HOST = 'aarch64-apple-darwin'


def bashes():
    """Every bash on the machine, once each: macOS's 3.2 drops a NUL in a command substitution silently, 4.4 and later
    warn about it on stderr."""
    found = {}
    for path in (shutil.which('bash'), '/bin/bash', '/usr/bin/bash', '/opt/homebrew/bin/bash', '/usr/local/bin/bash'):
        if path and os.access(path, os.X_OK):
            found.setdefault(os.path.realpath(path), path)
    return list(found.values())


FAKES = {
    # A Mac: Darwin on FAKE_ARCH (default arm64).
    'uname': '''#!/bin/sh
case "$1" in
  -m) echo "${FAKE_ARCH:-arm64}" ;;
  *) echo Darwin ;;
esac
''',
    # Records each build and leaves a static library named after its target where the script looks for it.
    'cargo': '''#!/bin/sh
target=
while [ $# -gt 0 ]; do [ "$1" = --target ] && target=$2; shift; done
echo "$target" >> "$FAKE_LOG/cargo"
mkdir -p "$CARGO_TARGET_DIR/$target/release"
echo "lib $target" > "$CARGO_TARGET_DIR/$target/release/libpersona_core.a"
''',
    'rustup': '''#!/bin/sh
echo "$*" >> "$FAKE_LOG/rustup"
''',
    'rustc': '''#!/bin/sh
echo "host: %s"
''' % HOST,
    'xcrun': '''#!/bin/sh
case "$*" in
  *--show-sdk-build-version*) echo "${FAKE_SDK_BUILD:-23A100}" ;;
  *--show-sdk-path*) echo /fake/sdk ;;
esac
''',
    # -create-xcframework: one folder per library, named after the target, and an Info.plist.
    'xcodebuild': '''#!/bin/sh
[ "$1" = -create-xcframework ] || exit 0
shift
output=
libraries=
while [ $# -gt 0 ]; do
  case "$1" in
    -library) libraries="$libraries $2"; shift ;;
    -headers) shift ;;
    -output) output=$2; shift ;;
  esac
  shift
done
[ ! -e "$output" ] || { echo "exists: $output" >&2; exit 1; }
mkdir -p "$output"
for library in $libraries; do
  slice=$(basename "$(dirname "$(dirname "$library")")")
  mkdir -p "$output/$slice"
  cp "$library" "$output/$slice/"
done
echo plist > "$output/Info.plist"
echo "$output" >> "$FAKE_LOG/xcframework"
''',
}


@unittest.skipUnless(SHASUM, 'needs shasum')
class AppleCoreBuildTests(unittest.TestCase):
    def setUp(self):
        self.temp = Path(tempfile.mkdtemp(prefix='persona-core-apple-test-'))
        self.addCleanup(shutil.rmtree, self.temp)
        self.cache = self.temp / 'cache'
        self.logs = self.temp / 'logs'
        self.logs.mkdir()
        bin_dir = self.temp / 'bin'
        bin_dir.mkdir()
        for name, body in FAKES.items():
            (bin_dir / name).write_text(body)
            (bin_dir / name).chmod(0o755)
        self.env = {
            'PATH': '%s:%s:/usr/bin:/bin' % (bin_dir, os.path.dirname(SHASUM)),
            'HOME': str(self.temp / 'home'),
            'TMPDIR': str(self.temp),
            'FAKE_LOG': str(self.logs),
            'PERSONA_APP_CORE_CACHE': str(self.cache),
        }

    def checkout(self, name, source='pub fn core() {}\n'):
        """A checkout with the files the script hashes, the script itself among them."""
        root = self.temp / name
        core = root / 'native/app-core'
        (core / 'scripts').mkdir(parents=True)
        shutil.copy2(SCRIPT, core / 'scripts/build-apple.sh')
        shutil.copy2(ROOT / 'native/app-core/rust-toolchain.toml', core / 'rust-toolchain.toml')
        (core / 'Cargo.toml').write_text('[workspace]\n')
        (core / 'Cargo.lock').write_text('# lock\n')
        for crate in ('persona-core', 'persona-core-runtime', 'persona-core-ffi'):
            (core / 'crates' / crate / 'src').mkdir(parents=True)
            (core / 'crates' / crate / 'src/lib.rs').write_text(source)
        (core / 'crates/persona-core-ffi/include').mkdir()
        (core / 'crates/persona-core-ffi/include/persona_core.h').write_text('int persona_core(void);\n')
        (root / 'apple/Packages/PersonaCore').mkdir(parents=True)
        return root

    def run_script(self, root, *args, bash='bash', **env):
        result = subprocess.run([bash, str(root / 'native/app-core/scripts/build-apple.sh'), *args],
                                env={**self.env, **env}, capture_output=True, text=True, timeout=120)
        return result

    def build(self, root, *args, **env):
        result = self.run_script(root, *args, **env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout

    def built(self):
        """The targets cargo built since the last call."""
        log = self.logs / 'cargo'
        targets = log.read_text().split() if log.exists() else []
        log.unlink(missing_ok=True)
        return targets

    def framework(self, root):
        dest = root / 'apple/Packages/PersonaCore/build/PersonaCoreC.xcframework'
        slices = sorted(p.name for p in dest.iterdir() if p.is_dir())
        stamp = (root / 'apple/Packages/PersonaCore/build/Inputs.stamp').read_text().split()
        return slices, stamp

    def entries(self):
        return sorted(p.name for p in self.cache.iterdir()) if self.cache.exists() else []

    def test_a_second_worktree_at_the_same_inputs_copies_the_cached_framework(self):
        first, second = self.checkout('first'), self.checkout('second')
        out = self.build(first, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET], 'a simulator request builds only its slice')
        self.assertIn('XCFramework (simulator) ready', out)
        self.assertEqual(self.framework(first), ([SIM_TARGET], [self.entries()[0], 'simulator']))

        out = self.build(first, '--simulator')
        self.assertIn('XCFramework is current', out)
        self.assertEqual(self.built(), [])

        # Another checkout path: the key holds paths relative to the checkout, so it is the same entry.
        out = self.build(second, '--simulator')
        self.assertEqual(self.built(), [], 'the second worktree must reuse the cached framework')
        self.assertIn('from the shared cache', out)
        self.assertEqual(self.framework(second), self.framework(first))
        self.assertEqual(len(self.entries()), 1)

    def test_a_full_request_builds_every_slice_and_serves_later_simulator_requests(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first, '--simulator')
        self.built()
        # The worktree has only the simulator slice: a full request (release, CI, swift test) builds all three.
        self.build(first)
        self.assertEqual(sorted(self.built()), sorted(['aarch64-apple-ios', SIM_TARGET, HOST]))
        slices, stamp = self.framework(first)
        self.assertEqual(slices, sorted(['aarch64-apple-ios', SIM_TARGET, HOST]))
        self.assertEqual(stamp[1], 'all')
        # A full worktree copy satisfies a simulator request as it is.
        self.assertIn('XCFramework is current', self.build(first, '--simulator'))
        # Another worktree asking for everything gets the full entry without building.
        self.build(second)
        self.assertEqual(self.built(), [])
        self.assertEqual(self.framework(second), self.framework(first))
        key = self.entries()[0]
        self.assertEqual(sorted(p.name for p in (self.cache / key).iterdir()), ['all', 'simulator'])

    def test_a_simulator_request_takes_a_full_entry_when_it_is_the_only_one(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first)
        self.built()
        out = self.build(second, '--simulator')
        self.assertEqual(self.built(), [])
        self.assertIn('XCFramework (all) from the shared cache', out)
        self.assertNotIn('simulator slice only', out)
        self.assertEqual(self.framework(second)[1][1], 'all')

    def test_a_simulator_only_framework_says_how_to_build_for_a_phone(self):
        # Xcode stops with "no library for this platform" on a phone or My Mac (Designed for iPad) when the worktree
        # has the simulator slice only, so every way of leaving one says how to add the rest; a full one says nothing.
        first, second = self.checkout('first'), self.checkout('second')
        note = 'simulator slice only; for a phone'
        self.assertIn(note, self.build(first, '--simulator'))        # built
        self.assertIn(note, self.build(first, '--simulator'))        # current
        self.assertIn(note, self.build(second, '--simulator'))       # from the cache
        out = self.build(first)
        self.assertIn('XCFramework (all) ready', out)
        self.assertNotIn(note, out)
        self.assertNotIn(note, self.build(first, '--simulator'))     # a full copy serves it as it is

    def test_changed_inputs_build_again_and_only_the_newest_keys_stay(self):
        first = self.checkout('first')
        # Something else in the cache folder is neither counted nor removed.
        (self.cache / 'notes').mkdir(parents=True)
        self.build(first, '--simulator', PERSONA_APP_CORE_CACHE_KEEP='2')
        old = [name for name in self.entries() if name != 'notes']
        for n in range(2):
            (first / 'native/app-core/crates/persona-core/src/lib.rs').write_text('pub fn core() { %d }\n' % n)
            self.build(first, '--simulator', PERSONA_APP_CORE_CACHE_KEEP='2')
            self.assertEqual(self.built(), [SIM_TARGET, SIM_TARGET] if n == 0 else [SIM_TARGET])
        self.assertEqual(len(self.entries()), 3)
        self.assertIn('notes', self.entries())
        self.assertNotIn(old[0], self.entries(), 'the oldest key is pruned')
        self.assertIn(self.framework(first)[1][0], self.entries())

    def test_the_environment_that_changes_the_output_is_part_of_the_key(self):
        first = self.checkout('first')
        self.build(first, '--simulator')
        self.build(first, '--simulator', FAKE_SDK_BUILD='24A200')
        self.build(first, '--simulator', RUSTFLAGS='-C opt-level=1')
        self.assertEqual(self.built(), [SIM_TARGET] * 3)
        self.assertEqual(len(self.entries()), 3)

    def test_the_simulator_slice_follows_the_macs_architecture(self):
        first = self.checkout('first')
        self.build(first, '--simulator', FAKE_ARCH='x86_64')
        self.assertEqual(self.built(), ['x86_64-apple-ios'])
        self.build(first, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertEqual(len(self.entries()), 2, 'the architecture is part of the key')
        result = self.run_script(first, '--simulator', FAKE_ARCH='aarch64')
        self.assertEqual(result.returncode, 1)
        self.assertIn('unsupported Mac architecture aarch64', result.stderr)

    def test_paths_are_part_of_the_key(self):
        first, second = self.checkout('first'), self.checkout('second')
        (first / 'native/app-core/crates/persona-core/src/a.rs').write_text('// same\n')
        (second / 'native/app-core/crates/persona-core/src/b.rs').write_text('// same\n')
        self.build(first, '--simulator')
        self.build(second, '--simulator')
        self.assertEqual(self.built(), [SIM_TARGET, SIM_TARGET])
        self.assertEqual(len(self.entries()), 2)

    def test_every_bash_builds_without_a_word_on_stderr_and_to_the_same_key(self):
        # The key's stream separates each path from its content with a NUL; captured in a command substitution, bash
        # 4.4+ warned "ignored null byte" on every run and dropped it.
        keys = set()
        for n, bash in enumerate(bashes()):
            with self.subTest(bash=bash):
                root = self.checkout('bash%d' % n)
                for _ in range(2):  # built (or from the cache), then current
                    result = self.run_script(root, '--simulator', bash=bash)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(result.stderr, '')
                self.assertIn('XCFramework is current', result.stdout)
                keys.add(self.framework(root)[1][0])
        self.assertEqual(len(keys), 1, keys)

    def test_a_cache_that_cannot_be_created_costs_only_the_cache(self):
        # A file where the cache's folder would go stops even root.
        first = self.checkout('first')
        blocker = self.temp / 'a-file'
        blocker.write_text('')
        result = self.run_script(first, '--simulator', PERSONA_APP_CORE_CACHE=str(blocker / 'cache'))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertIn('XCFramework (simulator) ready', result.stdout)
        self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)
        self.assertIn('could not write the shared cache', result.stderr)
        self.assertEqual(self.framework(first)[0], [SIM_TARGET])
        self.assertEqual(sorted(p.name for p in (first / 'apple/Packages/PersonaCore/build').iterdir()),
                         ['Inputs.stamp', 'PersonaCoreC.xcframework'])

    @unittest.skipIf(hasattr(os, 'geteuid') and os.geteuid() == 0, 'root reads and writes past the permissions')
    def test_a_read_only_cache_is_read_and_never_stops_a_build(self):
        first, second, third = self.checkout('first'), self.checkout('second'), self.checkout('third')
        self.build(first, '--simulator')
        self.built()
        entry = self.cache / self.entries()[0]
        locked = [self.cache, entry, entry / 'simulator']
        for path in locked:
            path.chmod(0o555)
        self.addCleanup(lambda: [path.chmod(0o755) for path in locked])
        # Read: copied as before, with nothing to say about the touch that failed.
        result = self.run_script(second, '--simulator')
        self.assertEqual((result.returncode, result.stderr), (0, ''))
        self.assertIn('from the shared cache', result.stdout)
        self.assertEqual(self.built(), [])
        # Write: every slice is built and kept in the worktree, with one line about the cache.
        result = self.run_script(third)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)
        self.assertIn('could not write the shared cache', result.stderr)
        self.assertEqual(self.framework(third)[1][1], 'all')
        self.assertEqual(self.entries(), [entry.name])

    @unittest.skipIf(hasattr(os, 'geteuid') and os.geteuid() == 0, 'root reads past the permissions')
    def test_an_entry_that_cannot_be_read_is_built_again(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first, '--simulator')
        self.built()
        library = self.cache / self.entries()[0] / 'simulator/PersonaCoreC.xcframework' / SIM_TARGET / 'libpersona_core.a'
        library.chmod(0)
        self.addCleanup(library.chmod, 0o644)
        result = self.run_script(second, '--simulator')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.built(), [SIM_TARGET])
        self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)
        self.assertIn('could not copy', result.stderr)
        self.assertEqual(self.framework(second)[0], [SIM_TARGET])
        self.assertEqual(sorted(p.name for p in (second / 'apple/Packages/PersonaCore/build').iterdir()),
                         ['Inputs.stamp', 'PersonaCoreC.xcframework'])

    def test_the_cache_can_be_turned_off(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first, '--simulator', PERSONA_APP_CORE_CACHE='0')
        self.build(second, '--simulator', PERSONA_APP_CORE_CACHE='0')
        self.assertEqual(self.built(), [SIM_TARGET, SIM_TARGET])
        self.assertFalse(self.cache.exists())
        self.assertFalse((self.temp / 'home').exists(), 'nothing is written to the default cache either')

    def test_unknown_arguments_and_a_bad_keep_count_are_refused(self):
        first = self.checkout('first')
        self.assertEqual(self.run_script(first, '--device').returncode, 2)
        result = self.run_script(first, PERSONA_APP_CORE_CACHE_KEEP='0')
        self.assertEqual(result.returncode, 1)
        self.assertIn('PERSONA_APP_CORE_CACHE_KEEP', result.stderr)
        self.assertEqual(self.built(), [])

    def test_no_scratch_is_left_behind(self):
        first, second = self.checkout('first'), self.checkout('second')
        self.build(first, '--simulator')
        self.build(second)
        for root in (first, second):
            self.assertEqual(sorted(p.name for p in (root / 'apple/Packages/PersonaCore/build').iterdir()),
                             ['Inputs.stamp', 'PersonaCoreC.xcframework'])
        for entry in self.cache.iterdir():
            self.assertEqual(sorted(p.name for p in entry.iterdir()), ['all', 'simulator'])
        self.assertEqual([p.name for p in self.temp.iterdir() if p.name.startswith('persona-core-apple-headers')], [])


if __name__ == '__main__':
    unittest.main()

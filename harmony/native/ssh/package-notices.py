#!/usr/bin/env python3
"""Bundle the checked Cargo notice inventory into the app's offline rawfile."""

import hashlib
import html
import json
import re
from pathlib import Path, PurePosixPath


PROJECT = Path(__file__).resolve().parent
LOCKFILE = PROJECT / 'Cargo.lock'
INVENTORY = PROJECT / 'licenses' / 'locked-inventory.json'
LICENSE_ROOT = PROJECT / 'licenses'
SUPPLEMENTAL_INVENTORY = LICENSE_ROOT / 'sdk-runtime' / 'supplemental-inventory.json'
OUTPUT = PROJECT.parent.parent / 'entry' / 'src' / 'main' / 'resources' / 'rawfile' / 'native-ssh-licenses.html'
PUBLIC_ATTRIBUTION = LICENSE_ROOT / 'public-attribution.json'
NAME_RE = re.compile(r'^[A-Za-z0-9][A-Za-z0-9_.-]*$')
VERSION_RE = re.compile(r'^[0-9][A-Za-z0-9.+-]*$')
SHA256_RE = re.compile(r'^[0-9a-f]{64}$')


def fail(message):
    raise SystemExit(message)


def field(block, name):
    match = re.search(r'^' + re.escape(name) + r'\s*=\s*"([^"]*)"\s*$', block, re.MULTILINE)
    return match.group(1) if match else None


def locked_registry_packages():
    try:
        lock_text = LOCKFILE.read_text(encoding='utf-8')
    except OSError as error:
        fail('Cargo.lock is unavailable: ' + str(error))
    blocks = re.split(r'(?m)^\[\[package\]\]\s*$', lock_text)[1:]
    packages = {}
    for block in blocks:
        name = field(block, 'name')
        version = field(block, 'version')
        source = field(block, 'source')
        if source is None:
            continue
        if not source.startswith('registry+'):
            fail('Unexpected non-registry locked dependency: ' + (name or '<unnamed>'))
        checksum = field(block, 'checksum')
        if not name or not version or not checksum or not SHA256_RE.match(checksum):
            fail('Malformed registry package entry in Cargo.lock')
        if not NAME_RE.match(name) or not VERSION_RE.match(version):
            fail('Unsafe locked crate identity: ' + name + '-' + version)
        identity = name + '-' + version
        if identity in packages:
            fail('Duplicate locked crate identity: ' + identity)
        packages[identity] = checksum
    if not packages:
        fail('Cargo.lock contains no registry dependencies')
    return packages


def checked_inventory(locked):
    try:
        entries = json.loads(INVENTORY.read_text(encoding='utf-8'))
    except (OSError, ValueError) as error:
        fail('Locked notice inventory is unavailable or invalid: ' + str(error))
    if not isinstance(entries, list) or not entries:
        fail('Locked notice inventory must be a non-empty list')

    seen = set()
    checked = []
    inventory_ids = set()
    for entry in entries:
        if not isinstance(entry, dict):
            fail('Malformed package entry in locked notice inventory')
        name, version = entry.get('name'), entry.get('version')
        if not isinstance(name, str) or not NAME_RE.match(name) or not isinstance(version, str) or not VERSION_RE.match(version):
            fail('Unsafe crate identity in locked notice inventory')
        identity = name + '-' + version
        if identity in inventory_ids:
            fail('Duplicate crate identity in locked notice inventory: ' + identity)
        inventory_ids.add(identity)
        if identity not in locked:
            fail('Inventory package is absent from Cargo.lock: ' + identity)
        if entry.get('registry_checksum') != locked[identity]:
            fail('Registry checksum differs from Cargo.lock: ' + identity)

        license_name = entry.get('license')
        if not isinstance(license_name, str) or not license_name.strip():
            fail('Declared license is missing: ' + identity)
        authors = entry.get('authors', [])
        if not isinstance(authors, list) or any(not isinstance(author, str) for author in authors):
            fail('Malformed authors for locked crate: ' + identity)
        repository = entry.get('repository', '')
        if not isinstance(repository, str):
            fail('Malformed repository for locked crate: ' + identity)
        notices = entry.get('notices')
        if not isinstance(notices, list) or not notices:
            fail('No license notice is listed for locked crate: ' + identity)

        package_dir = LICENSE_ROOT / identity
        if not package_dir.is_dir():
            fail('Notice directory is missing: ' + identity)
        resolved_root = LICENSE_ROOT.resolve()
        resolved_package_dir = package_dir.resolve()
        try:
            resolved_package_dir.relative_to(resolved_root)
        except ValueError:
            fail('Notice directory escapes the license inventory: ' + identity)

        checked_notices = []
        for notice in notices:
            if not isinstance(notice, dict):
                fail('Malformed notice entry for locked crate: ' + identity)
            raw_path = notice.get('path')
            expected_hash = notice.get('sha256')
            if not isinstance(raw_path, str) or '\\' in raw_path:
                fail('Unsafe notice path for locked crate: ' + identity)
            relative = PurePosixPath(raw_path)
            if str(relative) != raw_path or relative.is_absolute() or any(part in ('', '.', '..') for part in relative.parts):
                fail('Unsafe notice path for locked crate: ' + identity)
            if len(relative.parts) < 3 or relative.parts[0] != 'licenses' or relative.parts[1] != identity:
                fail('Notice path is outside its exact crate directory: ' + identity)
            if not isinstance(expected_hash, str) or not SHA256_RE.match(expected_hash):
                fail('Malformed SHA256 for notice in ' + identity)
            if raw_path in seen:
                fail('Notice path is listed more than once: ' + raw_path)
            seen.add(raw_path)

            unresolved = PROJECT.joinpath(*relative.parts)
            current = PROJECT
            for part in relative.parts:
                current = current / part
                if current.is_symlink():
                    fail('Notice path contains a symbolic link: ' + raw_path)
            path = unresolved.resolve()
            try:
                path.relative_to(resolved_package_dir)
            except ValueError:
                fail('Notice file escapes its exact crate directory: ' + raw_path)
            if not path.is_file():
                fail('Notice file is missing: ' + raw_path)
            content = path.read_bytes()
            actual_hash = hashlib.sha256(content).hexdigest()
            if actual_hash != expected_hash:
                fail('Notice SHA256 mismatch: ' + raw_path)
            try:
                text = content.decode('utf-8')
            except UnicodeDecodeError:
                fail('Notice is not UTF-8 text: ' + raw_path)
            note = notice.get('note', '')
            if not isinstance(note, str):
                fail('Malformed attribution note for notice in ' + identity)
            checked_notices.append({
                'path': '/'.join(relative.parts[2:]),
                'sha256': actual_hash,
                'text': text,
                'note': note,
            })

        checked.append({
            'name': name,
            'version': version,
            'identity': identity,
            'license': license_name,
            'authors': authors,
            'repository': repository,
            'notices': sorted(checked_notices, key=lambda item: item['path']),
        })

    if inventory_ids != set(locked):
        missing = sorted(set(locked) - inventory_ids)
        extra = sorted(inventory_ids - set(locked))
        fail('Locked inventory does not match Cargo.lock; missing=' + ','.join(missing) + ' extra=' + ','.join(extra))
    return sorted(checked, key=lambda item: (item['name'].casefold(), item['version']))


def checked_supplemental_inventory():
    try:
        manifest = json.loads(SUPPLEMENTAL_INVENTORY.read_text(encoding='utf-8'))
    except (OSError, ValueError) as error:
        fail('Supplemental notice inventory is unavailable or invalid: ' + str(error))
    if not isinstance(manifest, dict) or manifest.get('schema_version') != 1:
        fail('Unsupported SDK supplemental inventory format')
    entries = manifest.get('items')
    if not isinstance(entries, list) or not entries:
        fail('SDK supplemental inventory must contain at least one item')

    checked = []
    seen_items = set()
    seen_paths = set()
    runtime_root = LICENSE_ROOT / 'sdk-runtime'
    resolved_root = runtime_root.resolve()
    for entry in entries:
        if not isinstance(entry, dict):
            fail('Malformed item in SDK supplemental inventory')
        name, version = entry.get('name'), entry.get('version')
        if not isinstance(name, str) or not name.strip() or not isinstance(version, str) or not VERSION_RE.match(version):
            fail('Malformed SDK runtime identity')
        identity = name + '-' + version
        if identity in seen_items:
            fail('Duplicate SDK runtime identity: ' + identity)
        seen_items.add(identity)
        license_name = entry.get('license')
        authors = entry.get('authors')
        repository = entry.get('repository', '')
        source_path = entry.get('source_sdk_path')
        source_version = entry.get('source_sdk_version')
        source_hash = entry.get('source_content_sha256')
        provenance = entry.get('provenance')
        source_files = entry.get('source_files')
        notices = entry.get('notices')
        if not isinstance(license_name, str) or not license_name.strip():
            fail('SDK runtime license is missing: ' + identity)
        if not isinstance(authors, list) or any(not isinstance(author, str) for author in authors):
            fail('Malformed SDK runtime attribution: ' + identity)
        if not isinstance(repository, str):
            fail('Malformed SDK runtime repository: ' + identity)
        if not isinstance(source_version, str) or not source_version.strip():
            fail('SDK source version is missing: ' + identity)
        if source_version != version:
            fail('SDK runtime version differs from its pinned source version: ' + identity)
        if not isinstance(source_hash, str) or not SHA256_RE.match(source_hash):
            fail('Malformed SDK source content SHA256: ' + identity)
        if not isinstance(provenance, str) or not provenance.strip():
            fail('SDK runtime provenance is missing: ' + identity)
        if not isinstance(source_files, list) or not source_files:
            fail('SDK runtime source file hashes are missing: ' + identity)
        if not isinstance(notices, list) or not notices:
            fail('SDK runtime notices are missing: ' + identity)

        if not isinstance(source_path, str) or '\\' in source_path:
            fail('Malformed SDK source path: ' + identity)
        source_relative = PurePosixPath(source_path.split('#', 1)[0])
        if str(source_relative) != source_path.split('#', 1)[0] or source_relative.is_absolute() or any(part in ('', '.', '..') for part in source_relative.parts):
            fail('Unsafe SDK source path: ' + identity)
        source_file_paths = set()
        for source in source_files:
            if not isinstance(source, dict):
                fail('Malformed SDK source file metadata: ' + identity)
            sdk_path = source.get('sdk_path')
            sdk_hash = source.get('source_content_sha256')
            if not isinstance(sdk_path, str) or '\\' in sdk_path:
                fail('Malformed SDK source file path: ' + identity)
            sdk_relative = PurePosixPath(sdk_path)
            if sdk_relative.is_absolute() or any(part in ('', '.', '..') for part in sdk_relative.parts) or str(sdk_relative) != sdk_path:
                fail('Unsafe SDK source file path: ' + identity)
            if not isinstance(sdk_hash, str) or not SHA256_RE.match(sdk_hash):
                fail('Malformed SDK source file SHA256: ' + identity)
            if sdk_path in source_file_paths:
                fail('Duplicate SDK source file path: ' + sdk_path)
            source_file_paths.add(sdk_path)
        if source_relative.as_posix() not in source_file_paths:
            fail('Primary SDK source path is not pinned in source_files: ' + identity)

        checked_notices = []
        for notice in notices:
            if not isinstance(notice, dict):
                fail('Malformed SDK runtime notice entry: ' + identity)
            raw_path = notice.get('path')
            expected_hash = notice.get('sha256')
            if not isinstance(raw_path, str) or '\\' in raw_path:
                fail('Unsafe SDK runtime notice path: ' + identity)
            relative = PurePosixPath(raw_path)
            if str(relative) != raw_path or relative.is_absolute() or any(part in ('', '.', '..') for part in relative.parts):
                fail('Unsafe SDK runtime notice path: ' + identity)
            if len(relative.parts) < 3 or relative.parts[:2] != ('licenses', 'sdk-runtime'):
                fail('SDK runtime notice is outside licenses/sdk-runtime: ' + identity)
            if raw_path in seen_paths:
                fail('SDK runtime notice path is listed more than once: ' + raw_path)
            seen_paths.add(raw_path)
            if not isinstance(expected_hash, str) or not SHA256_RE.match(expected_hash):
                fail('Malformed SDK runtime notice SHA256: ' + raw_path)

            unresolved = PROJECT.joinpath(*relative.parts)
            current = PROJECT
            for part in relative.parts:
                current = current / part
                if current.is_symlink():
                    fail('SDK runtime notice path contains a symbolic link: ' + raw_path)
            path = unresolved.resolve()
            try:
                path.relative_to(resolved_root)
            except ValueError:
                fail('SDK runtime notice escapes licenses/sdk-runtime: ' + raw_path)
            if not path.is_file():
                fail('SDK runtime notice is missing: ' + raw_path)
            content = path.read_bytes()
            actual_hash = hashlib.sha256(content).hexdigest()
            if actual_hash != expected_hash:
                fail('SDK runtime notice SHA256 mismatch: ' + raw_path)
            if len(notices) == 1 and source_hash != actual_hash:
                fail('SDK source content SHA256 does not match its retained notice: ' + identity)
            try:
                text = content.decode('utf-8')
            except UnicodeDecodeError:
                fail('SDK runtime notice is not UTF-8 text: ' + raw_path)
            checked_notices.append({
                'path': '/'.join(relative.parts[2:]),
                'sha256': actual_hash,
                'text': text,
                'note': '',
            })

        checked.append({
            'name': name,
            'version': version,
            'license': license_name,
            'authors': authors,
            'repository': repository,
            'source_sdk_path': source_path,
            'source_sdk_version': source_version,
            'provenance': provenance,
            'notices': sorted(checked_notices, key=lambda item: item['path']),
        })
    return sorted(checked, key=lambda item: (item['name'].casefold(), item['version']))


def esc(value):
    return html.escape(value, quote=True)


def render(entries):
    chunks = [
        '<!doctype html>',
        '<html lang="en">',
        '<head>',
        '  <meta charset="utf-8">',
        '  <meta name="viewport" content="width=device-width, initial-scale=1">',
        '  <meta http-equiv="Content-Security-Policy" content="default-src \'none\'; img-src \'none\'; media-src \'none\'; font-src \'none\'; connect-src \'none\'; script-src \'none\'; style-src \'unsafe-inline\'; object-src \'none\'; base-uri \'none\'; form-action \'none\'">',
        '  <meta name="color-scheme" content="light dark">',
        '  <title>Open source licenses</title>',
        '  <style>',
        '    :root { color-scheme: light dark; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #fff; color: #202124; }',
        '    * { box-sizing: border-box; }',
        '    body { margin: 0; padding: 20px 18px 40px; font-size: 16px; line-height: 1.55; }',
        '    main { max-width: 900px; margin: 0 auto; }',
        '    article { padding: 18px 0 22px; border-top: 1px solid #d9dce1; }',
        '    h2 { margin: 0; font-size: 1.2rem; overflow-wrap: anywhere; }',
        '    .version { margin: 3px 0 12px; color: #5d626a; }',
        '    dl { display: grid; grid-template-columns: minmax(110px, 150px) minmax(0, 1fr); gap: 5px 12px; margin: 0 0 16px; }',
        '    dt { font-weight: 600; }',
        '    dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }',
        '    .notice { margin-top: 16px; }',
        '    h3 { margin: 0 0 3px; font-size: 1rem; overflow-wrap: anywhere; }',
        '    .checksum, .note { margin: 0 0 8px; color: #5d626a; font-size: .85rem; overflow-wrap: anywhere; }',
        '    pre { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; font: 0.88rem/1.5 ui-monospace, "SFMono-Regular", Consolas, monospace; }',
        '    @media (prefers-color-scheme: dark) { :root { background: #121316; color: #eceef2; } .version, .checksum, .note { color: #b3b8c2; } article { border-color: #3b3e45; } }',
        '  </style>',
        '</head>',
        '<body>',
        '<main>',
    ]
    for entry in entries:
        authors = ', '.join(entry['authors']) if entry['authors'] else entry['name']
        repository = entry['repository'] if entry['repository'] else 'Not declared'
        chunks.extend([
            '  <article>',
            '    <h2>' + esc(entry['name']) + '</h2>',
            '    <p class="version">Version ' + esc(entry['version']) + '</p>',
            '    <dl>',
            '      <dt>Declared license</dt><dd>' + esc(entry['license']) + '</dd>',
            '      <dt>Attribution</dt><dd>' + esc(authors) + '</dd>',
            '      <dt>Repository</dt><dd>' + esc(repository) + '</dd>',
        ])
        if entry.get('source_sdk_path'):
            source = 'HarmonyOS Native SDK ' + entry['source_sdk_version'] + ' · ' + entry['source_sdk_path']
            chunks.append('      <dt>Source</dt><dd>' + esc(source) + '</dd>')
        chunks.append('    </dl>')
        if entry.get('provenance'):
            chunks.append('    <p class="note">' + esc(entry['provenance']) + '</p>')
        for notice in entry['notices']:
            chunks.extend([
                '    <section class="notice">',
                '      <h3>' + esc(notice['path']) + '</h3>',
                '      <p class="checksum">SHA-256: ' + notice['sha256'] + '</p>',
            ])
            if notice['note']:
                chunks.append('      <p class="note">' + esc(str(notice['note'])) + '</p>')
            chunks.extend([
                '      <pre>' + esc(notice['text']) + '</pre>',
                '    </section>',
            ])
        chunks.append('  </article>')
    chunks.extend(['</main>', '</body>', '</html>', ''])
    return '\n'.join(chunks)


def public_attribution_manifest(cargo_entries, supplemental_entries, html_bytes):
    """Pin only the public license inputs and the exact generated offline bundle."""
    repo_root = PROJECT.parents[2]

    def relative(path):
        return path.relative_to(repo_root).as_posix()

    notices = []
    for entry in cargo_entries:
        for notice in entry['notices']:
            notices.append({
                'path': relative(LICENSE_ROOT / entry['identity'] / notice['path']),
                'sha256': notice['sha256'],
            })
    for entry in supplemental_entries:
        for notice in entry['notices']:
            notices.append({
                'path': relative(LICENSE_ROOT / 'sdk-runtime' / notice['path']),
                'sha256': notice['sha256'],
            })
    notices.sort(key=lambda item: item['path'])

    inventories = []
    for path in (INVENTORY, SUPPLEMENTAL_INVENTORY):
        inventories.append({
            'path': relative(path),
            'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
        })

    return {
        'schema_version': 1,
        'inventories': inventories,
        'notices': notices,
        'generated_html': {
            'path': relative(OUTPUT),
            'sha256': hashlib.sha256(html_bytes).hexdigest(),
        },
    }


def main():
    locked = locked_registry_packages()
    cargo_entries = checked_inventory(locked)
    supplemental_entries = checked_supplemental_inventory()
    entries = cargo_entries + supplemental_entries
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    html_bytes = render(entries).encode('utf-8')
    OUTPUT.write_bytes(html_bytes)
    manifest = public_attribution_manifest(cargo_entries, supplemental_entries, html_bytes)
    with PUBLIC_ATTRIBUTION.open('w', encoding='utf-8', newline='\n') as output:
        output.write(json.dumps(manifest, ensure_ascii=False, sort_keys=True, indent=2) + '\n')
    cargo_notices = sum(len(entry['notices']) for entry in cargo_entries)
    supplemental_notices = sum(len(entry['notices']) for entry in supplemental_entries)
    print('Wrote ' + str(OUTPUT) + ' (' + str(len(cargo_entries)) + ' Cargo packages, ' +
          str(cargo_notices) + ' Cargo notices, ' + str(len(supplemental_entries)) +
          ' SDK runtime components, ' + str(supplemental_notices) + ' supplemental notices)')


if __name__ == '__main__':
    main()

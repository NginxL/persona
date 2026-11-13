#!/usr/bin/env python3
"""Copy public registry license notices for exactly the reviewed Cargo.lock; no network or cargo invocation."""
import argparse
import hashlib
import json
import shutil
import tomllib
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument('--registry-src', type=Path, required=True, help='Cargo registry/src directory, or its index child')
args = parser.parse_args()
project = Path(__file__).resolve().parent
lock = tomllib.loads((project / 'Cargo.lock').read_text())
upstream = json.loads((project / 'licenses' / 'upstream-notices.json').read_text())
roots = [args.registry_src] + sorted(path for path in args.registry_src.iterdir() if path.is_dir())
entries = []
for package in lock['package']:
    if 'source' not in package:
        continue
    if not package['source'].startswith('registry+'):
        raise SystemExit('Unexpected non-registry dependency: ' + package['name'])
    identity = package['name'] + '-' + package['version']
    source = next((root / identity for root in roots if (root / identity / 'Cargo.toml').is_file()), None)
    if source is None:
        raise SystemExit('Public dependency source missing from offline cache: ' + identity)
    manifest = tomllib.loads((source / 'Cargo.toml').read_text())['package']
    output = project / 'licenses' / identity
    output.mkdir(parents=True, exist_ok=True)
    notice_files = [path for path in source.rglob('*') if path.is_file() and path.name.upper().startswith(('LICENSE', 'COPYING', 'NOTICE'))]
    explicit = manifest.get('license-file')
    if explicit and (source / explicit).is_file() and source / explicit not in notice_files:
        notice_files.append(source / explicit)
    notices = []
    for path in sorted(notice_files):
        relative = path.relative_to(source)
        destination = output / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, destination)
        notices.append({'path': str(destination.relative_to(project)), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()})
    # Some published crates exclude their repository's license files. Only admit
    # separately retained upstream notices pinned to that exact published VCS commit.
    if not notices and identity in upstream:
        retained = upstream[identity]
        vcs = json.loads((source / '.cargo_vcs_info.json').read_text())
        if retained['license'] != manifest.get('license') or retained['git_commit'] != vcs['git']['sha1']:
            raise SystemExit('Upstream notice identity does not match the locked crate: ' + identity)
        for item in retained['files']:
            path = (project / item['path']).resolve()
            if not path.is_relative_to((project / 'licenses' / identity).resolve()) or not path.is_file():
                raise SystemExit('Retained upstream notice is missing or outside its crate directory: ' + identity)
            if hashlib.sha256(path.read_bytes()).hexdigest() != item['sha256']:
                raise SystemExit('Retained upstream notice checksum changed: ' + identity)
            notices.append(dict(item, note='Official upstream notice at the published crate VCS commit.'))
    if not notices:
        raise SystemExit('No license text found; review this dependency before release: ' + identity)
    # Preserve separately retained attribution instead of dropping it from the generated inventory.
    attribution = output / 'NOTICE'
    if attribution.is_file() and not any(item['path'] == str(attribution.relative_to(project)) for item in notices):
        notices.append({'path': str(attribution.relative_to(project)), 'sha256': hashlib.sha256(attribution.read_bytes()).hexdigest()})
    entries.append({'name': package['name'], 'version': package['version'], 'registry_checksum': package.get('checksum', ''),
                    'license': manifest.get('license', ''), 'repository': manifest.get('repository', ''),
                    'authors': manifest.get('authors', []), 'notices': notices})
(project / 'licenses' / 'locked-inventory.json').write_text(json.dumps(entries, indent=2) + '\n')
lines = ['# Locked native SSH dependencies', '', 'Generated from Cargo.lock and cached public crate license files. No private implementation code is included.', '',
         '| Package | Version | Declared license | Notices |', '| --- | --- | --- | --- |']
for entry in entries:
    lines.append('| ' + entry['name'] + ' | ' + entry['version'] + ' | ' + entry['license'] + ' | [' + str(len(entry['notices'])) + ' files](licenses/' + entry['name'] + '-' + entry['version'] + '/) |')
(project / 'THIRD_PARTY_NOTICES.md').write_text('\n'.join(lines) + '\n')

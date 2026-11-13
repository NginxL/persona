#!/usr/bin/env python3
"""Collect public Cargo license notices for the Android core's locked registry closure."""
import argparse
import hashlib
import json
import subprocess
import sys
import tomllib
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workspace', type=Path, required=True)
    parser.add_argument('--registry-src', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    lock = tomllib.loads((args.workspace / 'Cargo.lock').read_text())
    # `metadata --filter-platform` still includes other targets' conditional packages. cargo tree's normal+build
    # edges name exactly the crates compiled for the two Android targets, excluding the typegen/dev-only workspace.
    selected = set()
    for target in ('aarch64-linux-android', 'x86_64-linux-android'):
        tree = subprocess.run(['cargo', '+1.98.0', 'tree', '--locked', '-p', 'persona-core-ffi', '--target', target,
                               '-e', 'normal,build', '--prefix', 'none'],
                              cwd=args.workspace, check=True, capture_output=True, text=True).stdout
        for line in tree.splitlines():
            fields = line.strip().split()
            if len(fields) >= 2 and fields[1].startswith('v'):
                selected.add((fields[0], fields[1][1:].split(' ', 1)[0]))
    upstream = json.loads((args.workspace / 'licenses/upstream-notices.json').read_text())
    entries = []
    documents = []
    for package in lock['package']:
        if (package['name'], package['version']) not in selected:
            continue
        if not package.get('source', '').startswith('registry+'):
            documents.append(('persona-' + package['name'], 'MIT', (args.workspace.parent.parent / 'LICENSE').read_bytes()))
            continue
        identity = package['name'] + '-' + package['version']
        roots = [args.registry_src, *sorted(path for path in args.registry_src.iterdir() if path.is_dir())]
        source = next((root / identity for root in roots if (root / identity / 'Cargo.toml').is_file()), None)
        if source is None:
            raise ValueError('Public dependency is missing from the offline registry: ' + identity)
        manifest = tomllib.loads((source / 'Cargo.toml').read_text())['package']
        files = [path for path in source.rglob('*') if path.is_file()
                 and path.name.upper().startswith(('LICENSE', 'COPYING', 'COPYRIGHT', 'NOTICE'))]
        notices = []
        explicit = manifest.get('license-file')
        if explicit and (source / explicit).is_file() and source / explicit not in files:
            files.append(source / explicit)
        if not files:
            # A published crate can omit its repository's license files. Admit only the retained public notice at the
            # exact VCS commit recorded in that crate, never a moving branch or a guessed replacement license.
            if identity not in upstream:
                raise ValueError('No license notice in the locked public dependency: ' + identity)
            retained = upstream[identity]
            vcs = json.loads((source / '.cargo_vcs_info.json').read_text())
            if (retained['license'] != manifest.get('license') or retained['repository'] != manifest.get('repository')
                    or retained['git_commit'] != vcs['git']['sha1']):
                raise ValueError('Upstream notice identity does not match the locked crate: ' + identity)
            for item in retained['files']:
                path = args.workspace / item['path']
                data = path.read_bytes()
                if hashlib.sha256(data).hexdigest() != item['sha256']:
                    raise ValueError('Upstream notice checksum changed: ' + str(path))
                relative = str(path.relative_to(args.workspace))
                notices.append({'file': relative, 'sha256': item['sha256'], 'source_url': item['source_url']})
                documents.append((identity, relative, data))
        for path in sorted(files):
            data = path.read_bytes()
            relative = str(path.relative_to(source))
            notices.append({'file': relative, 'sha256': hashlib.sha256(data).hexdigest()})
            documents.append((identity, relative, data))
        entries.append({'name': package['name'], 'version': package['version'],
                        'license': manifest.get('license', ''), 'notices': notices})
    output = ['Persona shared Rust core: third-party notices',
              'Generated from native/app-core/Cargo.lock and public registry sources.', '']
    for identity, relative, data in documents:
        output.extend(['=' * 72, identity + ' / ' + relative, '=' * 72,
                       data.decode('utf-8'), ''])
    result = {'third-party-notices.txt': '\n'.join(output).replace('\r\n', '\n').replace('\r', '\n') + '\n',
              'locked-notices.json': json.dumps(entries, indent=2) + '\n'}
    if args.check:
        for name, content in result.items():
            if not (args.output / name).is_file() or (args.output / name).read_text() != content:
                raise ValueError('Out-of-date or missing Android core notice: ' + name)
    else:
        args.output.mkdir(parents=True, exist_ok=True)
        for name, content in result.items():
            (args.output / name).write_text(content)
    print('Android core licenses: %d locked registry packages, %d notice files' % (len(entries), len(documents)))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, tomllib.TOMLDecodeError) as error:
        sys.exit('Android core licenses: FAILED (' + str(error) + ')')

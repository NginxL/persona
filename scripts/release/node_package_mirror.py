"""Persist standard consumer downloads within the selected automatic-release feed."""
import copy
import json
import os
from pathlib import Path
import shutil
import stat
import tempfile

from hub_release_paths import validate_release_paths
from node_artifacts import node_files, validate_node_manifest, verify_archive, verify_installed_nodes


def regular(path, directory=False):
    mode = path.lstat().st_mode
    if not (stat.S_ISDIR(mode) if directory else stat.S_ISREG(mode)):
        raise ValueError('Node download paths must not contain links or special files')


def consumer_manifest(manifest):
    validate_node_manifest(manifest)
    result = copy.deepcopy(manifest)
    for program, platform, artifact in node_files(result):
        artifact['name'] = f'{program}-{platform}.gz'
    return result


def mirror_root(root, namespace):
    feed = validate_release_paths(root, namespace)
    mirror = feed / 'node-packages'
    try:
        mirror.mkdir(mode=0o755)
    except FileExistsError:
        regular(mirror, directory=True)
    else:
        mirror.chmod(0o755)
    regular(mirror, directory=True)
    return mirror


def check_directory(directory, manifest):
    regular(directory, directory=True)
    standard = consumer_manifest(manifest)
    expected = {'manifest.json', 'source-manifest.json'}
    expected.update(artifact['name'] for _, _, artifact in node_files(standard))
    if {path.name for path in directory.iterdir()} != expected:
        raise ValueError('Unexpected immutable node download contents')
    for name in ('manifest.json', 'source-manifest.json'):
        regular(directory / name)
    if (json.loads((directory / 'manifest.json').read_text()) != standard
            or json.loads((directory / 'source-manifest.json').read_text()) != manifest):
        raise ValueError('Immutable node download manifest changed')
    for _, _, artifact in node_files(standard):
        verify_archive(directory, artifact)


def check_latest(mirror):
    latest = mirror / 'latest.json'
    if latest.exists() or latest.is_symlink():
        regular(latest)
        current = json.loads(latest.read_text())
        consumer_manifest(current)
        directory = mirror / current['version']
        regular(directory, directory=True)
        regular(directory / 'source-manifest.json')
        source = json.loads((directory / 'source-manifest.json').read_text())
        check_directory(directory, source)
        if current != consumer_manifest(source):
            raise ValueError('Unmanaged node download latest manifest')


def sync_directory(directory):
    descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def write_json(path, value):
    with path.open('x') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')
        stream.flush()
        os.fchmod(stream.fileno(), 0o644)
        os.fsync(stream.fileno())


def prepare_downloads(root, namespace, staged, manifest, native_directory):
    standard = consumer_manifest(manifest)
    mirror = mirror_root(root, namespace)
    check_latest(mirror)
    # The existing native preparation establishes platform/source identity. Reuse
    # those executables; never rebuild or execute a foreign-platform candidate.
    expected = Path(root) / 'releases/nodes' / manifest['version']
    if Path(native_directory) != expected:
        raise ValueError('Downloads must use the prepared native release')
    regular(expected.parent, directory=True)
    regular(expected, directory=True)
    verify_installed_nodes(expected, manifest)
    destination = mirror / manifest['version']
    if destination.exists() or destination.is_symlink():
        check_directory(destination, manifest)
        return destination
    temporary = Path(tempfile.mkdtemp(prefix='.stage-' + manifest['version'] + '-', dir=mirror))
    try:
        for program, platform, artifact in node_files(manifest):
            source = verify_archive(staged, artifact)
            target = temporary / f'{program}-{platform}.gz'
            with source.open('rb') as reader, target.open('xb') as writer:
                shutil.copyfileobj(reader, writer)
                writer.flush()
                os.fchmod(writer.fileno(), 0o644)
                os.fsync(writer.fileno())
        write_json(temporary / 'source-manifest.json', manifest)
        write_json(temporary / 'manifest.json', standard)
        check_directory(temporary, manifest)
        sync_directory(temporary)
        return temporary
    except BaseException:
        shutil.rmtree(temporary)
        raise


def activate_downloads(root, namespace, prepared, manifest, health):
    if (health.get('ok') is not True or health.get('version') != manifest['version']
            or health.get('build') != manifest['build']):
        raise RuntimeError('Matching healthy Hub required before download activation')
    mirror = mirror_root(root, namespace)
    check_latest(mirror)
    prepared = Path(prepared)
    version = consumer_manifest(manifest)['version']
    destination = mirror / version
    if (prepared.parent != mirror or '..' in prepared.parts
            or (prepared != destination and not prepared.name.startswith('.stage-' + version + '-'))):
        raise ValueError('Invalid prepared node download directory')
    check_directory(prepared, manifest)
    if prepared != destination:
        if destination.exists() or destination.is_symlink():
            check_directory(destination, manifest)
            shutil.rmtree(prepared)
        else:
            prepared.chmod(0o755)
            os.rename(prepared, destination)
        sync_directory(mirror)
    # Only a confirmed matching Hub reaches this call from the publisher. Versions
    # are immutable and retained; latest is the sole mutable consumer checkpoint.
    descriptor, name = tempfile.mkstemp(prefix='.latest-', dir=mirror)
    temporary = Path(name)
    try:
        with os.fdopen(descriptor, 'w') as stream:
            json.dump(consumer_manifest(manifest), stream, indent=2)
            stream.write('\n')
            stream.flush()
            os.fchmod(stream.fileno(), 0o644)
            os.fsync(stream.fileno())
        os.replace(temporary, mirror / 'latest.json')
        sync_directory(mirror)
    finally:
        temporary.unlink(missing_ok=True)
    return destination

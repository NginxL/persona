"""Collect only Apple failure diagnostics, never source/configuration or keys."""

import argparse
import json
from pathlib import Path
import shutil
import subprocess

import apple_launch_diagnostics


LOGS = ("simulator.log", "simulator-boot.log", "simulator-readiness.log",
        "regular-simulator.log", "regular-simulator-boot.log", "regular-readiness.log", "native-ssh-trace.log",
        "ios-unit.log", "ios-html-card.log", "ios-html-security.log",
        "ios-visual-compact.log", "ios-visual-regular.log")


def collect(output, destination, runner=subprocess.run, scope="all"):
    destination.mkdir(parents=True, exist_ok=True)
    logs = LOGS if scope != "regular" else (
        "regular-simulator.log", "regular-simulator-boot.log", "regular-readiness.log")
    for path in [output / name for name in logs] + sorted(output.glob("runtimes-*.json")):
        if path.is_file() and not path.is_symlink():
            shutil.copyfile(path, destination / path.name)
    for bundle in sorted(output.glob("*.xcresult")):
        if bundle.is_dir() and not bundle.is_symlink() and (scope != "regular" or bundle.name == "ios-regular-readiness.xcresult"):
            # Do not follow links out of a generated result bundle.
            shutil.copytree(bundle, destination / bundle.name, dirs_exist_ok=True,
                            ignore=lambda directory, names: [name for name in names
                                if (Path(directory) / name).is_symlink()])
    for name in ("live-readiness", "live-regular-readiness"):
        if scope == "regular" and name != "live-regular-readiness":
            continue
        source = output / "diagnostics" / name
        for path in sorted(source.glob("*")) if source.is_dir() else []:
            # The observer writes only bounded, allowlisted launch evidence. Copy
            # flat files without following links; no DerivedData or source enters.
            if path.is_file() and not path.is_symlink() and path.suffix in {".json", ".txt"}:
                live = destination / name
                live.mkdir(exist_ok=True)
                shutil.copyfile(path, live / path.name)
    for path in sorted((output / "diagnostics").glob("build-resources/*")) if (output / "diagnostics").is_dir() and scope != "regular" else []:
        # Bounded compiler metadata and resource samples only, never compiler
        # arguments, source files or private configuration.
        if path.is_file() and not path.is_symlink() and path.suffix in {".json", ".txt"}:
            resources = destination / "build-resources"
            resources.mkdir(exist_ok=True)
            shutil.copyfile(path, resources / path.name)
    if scope != "regular":
        apple_launch_diagnostics.collect(output, destination)
    else:
        # The regular observer already retained its own scoped launch collection.
        # Never read the earlier iPhone's root context in this shared job output.
        try:
            context = apple_launch_diagnostics.read_scope_file(output / "regular-readiness-context.json")
            json.loads(context)
        except (ValueError, TypeError):
            context = '{"unavailable":"regular iPad probe did not publish an owned regular-file context"}\n'
        apple_launch_diagnostics.publish(destination / "regular-readiness-context.json", context)
    commands = [
        ("simctl-list", ["xcrun", "simctl", "list", "--json"], 120, "json"),
        ("disk-space", ["df", "-h", str(output)], 30, "txt"),
    ]
    for name, command, timeout, suffix in commands:
        try:
            result = runner(command, capture_output=True, text=True, timeout=timeout)
            (destination / f"{name}.{suffix}").write_text(result.stdout)
            (destination / f"{name}.stderr.txt").write_text(result.stderr)
            (destination / f"{name}.status.txt").write_text(str(result.returncode) + "\n")
        except (OSError, subprocess.SubprocessError) as error:
            (destination / f"{name}.stderr.txt").write_text(str(error) + "\n")
            (destination / f"{name}.status.txt").write_text("unavailable\n")
    print(f"apple-ci: failure diagnostics: {destination}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--scope", choices=("all", "regular"), default="all")
    args = parser.parse_args()
    collect(args.output, args.destination, scope=args.scope)


if __name__ == "__main__":
    main()

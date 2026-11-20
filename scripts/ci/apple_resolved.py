"""Stage a Meta-specific Package.resolved only in a disposable CI source copy."""

import argparse
import hashlib
import json
from pathlib import Path


META_IDENTITY = "meta-wearables-dat-ios"


def render(document):
    """Match the existing Xcode resolved-file formatting byte for byte."""
    return (json.dumps(document, indent=2).replace('": ', '" : ') + "\n").encode("utf-8")


def sha256(content):
    return hashlib.sha256(content).hexdigest()


def stage(tracked, staged, meta, evidence):
    """Write only staged, preserving tracked and the original originHash."""
    if meta not in ("YES", "NO"):
        raise ValueError("Meta configuration must be YES or NO")
    if tracked.resolve() == staged.resolve():
        raise ValueError("the staged resolved file must be a disposable copy")

    tracked_before = tracked.read_bytes()
    staged_before = staged.read_bytes()
    if tracked_before != staged_before:
        raise ValueError("the staged resolved file must start byte-identical to tracked")

    original = json.loads(staged_before)
    document = json.loads(staged_before)
    meta_pins = [pin for pin in original["pins"] if pin["identity"] == META_IDENTITY]
    if len(meta_pins) != 1:
        raise ValueError(f"expected exactly one {META_IDENTITY} pin, found {len(meta_pins)}")

    if meta == "NO":
        document["pins"] = [pin for pin in document["pins"] if pin["identity"] != META_IDENTITY]
        staged_bytes = render(document)
        staged.write_bytes(staged_bytes)
    else:
        staged_bytes = staged_before

    result = json.loads(staged_bytes)
    if result["originHash"] != original["originHash"]:
        raise ValueError("originHash must remain unchanged")
    expected_pins = [pin for pin in original["pins"] if meta == "YES" or pin["identity"] != META_IDENTITY]
    if result["pins"] != expected_pins:
        raise ValueError("only the stale Meta pin may change in the default variant")
    if tracked.read_bytes() != tracked_before:
        raise RuntimeError("tracked Package.resolved was modified")

    lines = [
        f"meta_wearables={meta}",
        f"source_sha256={sha256(staged_before)}",
        f"staged_sha256={sha256(staged_bytes)}",
        f"source_pin_count={len(original['pins'])}",
        f"staged_pin_count={len(result['pins'])}",
        f"originHash={result['originHash']}",
        "tracked_unchanged=yes",
    ]
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text("\n".join(lines) + "\n")
    return lines


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tracked", type=Path, required=True)
    parser.add_argument("--staged", type=Path, required=True)
    parser.add_argument("--meta", choices=("YES", "NO"), required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    print("\n".join(stage(args.tracked, args.staged, args.meta, args.evidence)))


if __name__ == "__main__":
    main()

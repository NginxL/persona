"""Select an installed stable Apple runtime for the minimum compiler or the iOS deployment floor."""

import argparse
import json
from pathlib import Path
import re
import sys


def select(runtimes, platform="iOS", version="26.0"):
    pattern = re.escape(version) + (r"(?:\.[0-9]+)*" if version == "26.0" else "")
    compatible = []
    for runtime in runtimes:
        installed = runtime.get("version", "")
        identifier = runtime.get("identifier", "")
        name = runtime.get("name", "").lower()
        if (runtime.get("isAvailable") is True
                and identifier.startswith("com.apple.CoreSimulator.SimRuntime." + platform + "-")
                and re.fullmatch(pattern, installed)
                and "beta" not in name and "release candidate" not in name):
            compatible.append(runtime)
    return max(compatible, key=lambda runtime: tuple(int(part) for part in runtime["version"].split(".")))["identifier"] if compatible else None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("runtimes_json")
    parser.add_argument("--platform", choices=("iOS", "watchOS"), default="iOS")
    parser.add_argument("--version", choices=("26.0", "18.6"), default="26.0")
    args = parser.parse_args()
    selected = select(json.loads(Path(args.runtimes_json).read_text())["runtimes"], args.platform, args.version)
    if not selected:
        sys.exit("apple-ci: FAIL no available stable " + args.platform + " " + args.version + " runtime")
    print(selected)


if __name__ == "__main__":
    main()

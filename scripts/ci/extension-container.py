#!/usr/bin/env python3
"""Run real extension isolation in the hub runtime stage, without a hub or real state."""
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def run(args, timeout, **kwargs):
    print("+", " ".join(map(str, args)), flush=True)
    result = subprocess.run(args, timeout=timeout, **kwargs)
    if kwargs.get("capture_output"):
        print(result.stdout, end="", flush=True)
        print(result.stderr, end="", flush=True)
    result.check_returncode()
    return result


def main():
    image = ""
    with tempfile.TemporaryDirectory(prefix="persona-container-sandbox-") as tmp:
        directory = Path(tmp)
        # Non-root container users can traverse the binary's bind mount.
        directory.chmod(0o755)
        container = directory.name
        try:
            run(["docker", "build", "--target", "sandbox-runtime", "--iidfile", str(directory / "image"), "."],
                120, cwd=ROOT)
            image = (directory / "image").read_text().strip()
            # Parse the optional host policy in a disposable userspace; never load host LSM policy.
            policy = directory / "persona-hub.apparmor"
            policy.write_text((ROOT / "deploy/docker/persona-hub.apparmor").read_text())
            run(["docker", "run", "--rm", "--name", container + "-policy",
                 "--mount", "type=bind,src=" + str(directory) + ",dst=/probe,readonly", "ubuntu:24.04",
                 "/bin/sh", "-ec", "apt-get update -qq && DEBIAN_FRONTEND=noninteractive "
                 "apt-get install -y -qq --no-install-recommends apparmor && "
                 "apparmor_parser -Q -K /probe/persona-hub.apparmor"], 180)
            security = run(["docker", "info", "--format", "{{json .SecurityOptions}}"], 15,
                           capture_output=True, text=True)
            apparmor = ["--security-opt=apparmor=persona-hub"] if "apparmor" in security.stdout.lower() else []
            print("AppArmor: named host profile required" if apparmor else "AppArmor: disabled on this test host; profile syntax checked", flush=True)
            checks = {
                "sandbox": ["TestLinuxSandboxForReal"],
                "extmgr": ["TestExtensionSandbox", "TestExtensionSandboxMissing", "TestExtensionSandboxMissingOfficialExemption"],
            }
            for package in checks:
                binary = directory / (package + ".test")
                source = "./internal/extension/sandbox" if package == "sandbox" else "./internal/extmgr"
                run(["go", "test", "-c", "-o", str(binary), source],
                    180, cwd=ROOT / "server", env={**os.environ, "CGO_ENABLED": "0"})
                binary.chmod(0o755)
            result = run(["docker", "run", "--rm", "--name", container, "--network=none", "--cap-drop=ALL", "--user=1000:1000",
                          "--security-opt=no-new-privileges:true",
                          "--security-opt=seccomp=" + str(ROOT / "deploy/docker/extension-seccomp.json"),
                          *apparmor,
                          "--mount", "type=bind,src=" + str(directory) + ",dst=/probe,readonly",
                          "--env=HOME=/home/persona", image, "/bin/sh", "-ec",
                          'test "$(id -u)" != 0; '
                          'test ! -u /usr/bin/bwrap; '
                          'grep -q "^CapEff:[[:space:]]*0000000000000000$" /proc/self/status; '
                          '/probe/sandbox.test -test.run=^TestLinuxSandboxForReal$ -test.v -test.timeout=45s; '
                          '/probe/extmgr.test -test.run="^TestExtensionSandbox(Missing|MissingOfficialExemption)?$" -test.v -test.timeout=60s'],
                         120, capture_output=True, text=True)
            for tests in checks.values():
                for test in tests:
                    if "--- PASS: " + test + " (" not in result.stdout:
                        raise RuntimeError("real container isolation did not pass " + test)
            if "--- SKIP:" in result.stdout:
                raise RuntimeError("real container isolation skipped a required check")
            print("Extension container isolation: PASS (non-root, no capabilities, real filesystem/network isolation)")
        finally:
            subprocess.run(["docker", "rm", "-f", container, container + "-policy"], timeout=30, check=False, capture_output=True)
            if image:
                subprocess.run(["docker", "image", "rm", image], timeout=30, check=False)


if __name__ == "__main__":
    main()

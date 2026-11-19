#!/usr/bin/env python3
"""Run server tests without inheriting a live Persona profile or runtime configuration."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

RUNTIME_PREFIXES = ("PERSONA_", "JARVIS_", "PG")
# Test-only inputs may configure TLS and connection behavior, never a second target or external credential source.
DATABASE_QUERY_KEYS = {
    "sslmode", "sslrootcert", "sslcert", "sslkey", "sslpassword", "sslsni", "sslnegotiation",
    "connect_timeout", "target_session_attrs", "application_name",
}
# pgx uses os/user's home for defaults, not the sandbox HOME. Override implicit profile-file reads explicitly.
DATABASE_QUERY_DEFAULTS = {"passfile": "", "sslcert": "", "sslkey": "", "sslrootcert": ""}
# The hub's production database and the test database next to it: persona, or jarvis on a hub set up before the rename
# (#6149; scripts/deploy/db.sh keeps that database, and makes <database>_test in it).
TEST_DATABASES = {"/persona": "/persona_test", "/jarvis": "/jarvis_test"}
PROFILE_VARIABLES = {
    "CLAUDE_CONFIG_DIR", "CODEX_HOME", "CODEX_CONFIG_DIR",
    "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
    "BASH_ENV", "ENV", "ZDOTDIR",
}
# Service routing must not redirect synthetic DNS, loopback Git servers or
# sandbox assertions. Fixtures may still configure their own proxy after launch.
PROXY_VARIABLES = {"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}


def test_database_url(config_file):
    with open(config_file) as source:
        config = json.load(source)
    value = config.get("database_url", "")
    if not isinstance(value, str) or any(ord(character) <= 32 or ord(character) == 127 for character in value):
        raise ValueError("hub database_url must be a Postgres URL")
    parsed = urlsplit(value)
    if parsed.scheme not in {"postgres", "postgresql"} or not parsed.netloc or parsed.fragment:
        raise ValueError("hub database_url must be a Postgres URL")
    if parsed.path not in TEST_DATABASES:
        raise ValueError("server tests require the hub's production database configuration (persona, or jarvis before the rename)")
    # Go's URL query parser decodes keys and picks the first value; validating the path alone misses overrides.
    if ";" in parsed.query or re.search(r"%(?![0-9A-Fa-f]{2})", parsed.query):
        raise ValueError("invalid server test database query")
    pairs = parse_qsl(parsed.query, keep_blank_values=True, strict_parsing=True, errors="strict") if parsed.query else []
    settings = {}
    for key, setting in pairs:
        if key not in DATABASE_QUERY_KEYS or key in settings:
            raise ValueError("unsupported or repeated server test database query option")
        settings[key] = setting
    for key, setting in DATABASE_QUERY_DEFAULTS.items():
        settings.setdefault(key, setting)
    test_url = urlunsplit(parsed._replace(path=TEST_DATABASES[parsed.path], query=urlencode(settings)))
    if test_url == value:
        raise ValueError("server test database must differ from the production database")
    return test_url


def clean_environment(environment):
    cleaned = {key: value for key, value in environment.items()
               if not key.startswith(RUNTIME_PREFIXES) and key not in PROFILE_VARIABLES
               and key.upper() not in PROXY_VARIABLES}
    roots = [Path(value).expanduser().resolve() for key, value in environment.items()
             if key in {"PERSONA_HOME", "JARVIS_HOME"} and value]
    if roots and "PATH" in cleaned:
        entries = []
        for entry in cleaned["PATH"].split(os.pathsep):
            if not entry:
                continue
            path = Path(entry).expanduser().resolve()
            if any(path == root or root in path.parents for root in roots):
                continue
            entries.append(entry)
        cleaned["PATH"] = os.pathsep.join(entries)
    return cleaned


def with_trimpath(goflags):
    """GOFLAGS with -trimpath added unless it already sets the flag, -trimpath=false included (go_trimpath.sh)."""
    if any(flag.lstrip("-").split("=", 1)[0] == "trimpath" for flag in goflags.split()):
        return goflags
    return (goflags + " -trimpath").strip()


def run_tests(config_file, command, environment=None):
    database = test_database_url(config_file)
    parent = os.environ if environment is None else environment
    environment = clean_environment(parent)
    # Resolve only build/toolchain cache locations before replacing HOME. Never carry the service profile into Go.
    # Toolchain resolution may need the parent's network route; only test children
    # must not inherit it. Keep runtime/profile isolation in both environments.
    toolchain_environment = dict(environment)
    toolchain_environment.update({key: value for key, value in parent.items() if key.upper() in PROXY_VARIABLES})
    result = subprocess.run(["go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH", "GOFLAGS"],
                            env=toolchain_environment, capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise ValueError("could not resolve Go build cache locations")
    caches = json.loads(result.stdout)
    if not isinstance(caches, dict):
        raise ValueError("invalid Go build cache locations")
    for key in ("GOCACHE", "GOMODCACHE", "GOPATH"):
        value = caches.get(key)
        if not isinstance(value, str) or not value:
            raise ValueError("invalid Go build cache locations")
        environment[key] = value
    # The temporary HOME below hides a `go env -w GOFLAGS`, so the effective value goes on explicitly, with -trimpath:
    # without it the cache key holds each checkout's directory and no two checkouts share a compiled package (#10614).
    goflags = caches.get("GOFLAGS") or ""
    if not isinstance(goflags, str):
        raise ValueError("invalid Go build flags")
    environment["GOFLAGS"] = with_trimpath(goflags)
    temp_dir = "/tmp" if os.path.isdir("/tmp") else None
    # Go makes t.TempDir()'s directories, its build directory and the test binary with the process's umask. Under 002
    # (Ubuntu's default for a user with their own group) they are group-writable, and node's external adapter refuses
    # its own test fixtures there (external_adapter_unix.go). The tests run as on a 022 host (#10280).
    previous = os.umask(0o022)
    os.umask(previous | 0o022)
    try:
        with tempfile.TemporaryDirectory(prefix="pst-", dir=temp_dir) as directory:
            home = Path(directory).resolve()
            temporary = home / "tmp"
            temporary.mkdir()
            environment.update(HOME=str(home), TMPDIR=str(temporary), TMP=str(temporary), TEMP=str(temporary),
                               GOTMPDIR=str(temporary), PERSONA_TEST_DATABASE_URL=database, JARVIS_TEST_DATABASE_URL=database)
            return subprocess.run(command, env=environment).returncode
    finally:
        os.umask(previous)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database-file", required=True)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        parser.error("a test command is required")
    try:
        return run_tests(args.database_file, command)
    except (OSError, ValueError, subprocess.TimeoutExpired):
        # Exceptions can contain credentials or profile paths; never echo config values or captured command output.
        print("server test environment preparation failed; tests were not started", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())

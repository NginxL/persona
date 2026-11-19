#!/usr/bin/env python3
"""Prepare private CI release configuration from protected environment values.

Only an explicitly authorized release job calls this. Ordinary release-PR builds
use public configuration and never receive private key material. Files stay in a
private temporary directory and are not included in release artifacts or logs.
"""
from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import re
import sys

SHA256 = re.compile(r"[0-9a-f]{64}\Z")


class ConfigurationError(ValueError):
    pass


def required(name, env):
    value = env.get(name, "")
    if not isinstance(value, str) or not value.strip():
        raise ConfigurationError("protected release environment is missing " + name)
    return value


def write_private(path, data, mode=0o600):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_bytes(data)
    path.chmod(mode)


def prepare_android(directory, env):
    certificate = required("PERSONA_ANDROID_CERTIFICATE_SHA256", env).lower()
    if not SHA256.fullmatch(certificate):
        raise ConfigurationError("Android certificate digest is invalid")
    keystore = directory / "android-release.jks"
    raw = base64.b64decode(required("PERSONA_ANDROID_KEYSTORE_BASE64", env), validate=True)
    if not raw:
        raise ConfigurationError("Android keystore must not be empty")
    write_private(keystore, raw)
    repository = required("GITHUB_REPOSITORY", env)
    workspace = env.get("GITHUB_WORKSPACE", "")
    if not workspace or not Path(workspace).is_dir():
        raise ConfigurationError("Android release must use the checked-out fixed-source workspace")
    config = {
        "repository": repository,
        "git_url": "https://github.com/" + repository + ".git",
        "checkout": workspace,
        "branch": "main",
        "application_id": required("PERSONA_ANDROID_APPLICATION_ID", env),
        "certificate_sha256": certificate,
        "state_directory": str(directory / "state"),
        "build_environment": {"ANDROID_HOME": env.get("ANDROID_HOME", env.get("ANDROID_SDK_ROOT", ""))},
        "local_signing": {
            "keystore": str(keystore), "alias": required("PERSONA_ANDROID_KEY_ALIAS", env),
            "password_command": ["/bin/sh", "-c", 'printf %s "$PERSONA_ANDROID_KEYSTORE_PASSWORD"'],
            "key_password_command": ["/bin/sh", "-c", 'printf %s "$PERSONA_ANDROID_KEY_PASSWORD"'],
        },
    }
    if not config["build_environment"]["ANDROID_HOME"]:
        raise ConfigurationError("Android SDK path is missing")
    path = directory / "android.json"
    write_private(path, (json.dumps(config, indent=2) + "\n").encode())
    return path


def prepare_apple(directory, env):
    key = directory / "AuthKey.p8"
    write_private(key, required("PERSONA_APPLE_API_KEY", env).encode())
    bundle_id = required("PERSONA_APP_BUNDLE_ID", env)
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]+", bundle_id):
        raise ConfigurationError("Apple bundle ID is invalid")
    external = json.loads(required("PERSONA_TESTFLIGHT_EXTERNAL_JSON", env))
    if not isinstance(external, dict):
        raise ConfigurationError("TestFlight external configuration must be a JSON object")
    config = {
        "app": {"bundle_id": bundle_id, "team_id": required("PERSONA_APPLE_TEAM_ID", env)},
        "app_store": {
            "key_id": required("PERSONA_APPLE_API_KEY_ID", env),
            "issuer_id": required("PERSONA_APPLE_API_ISSUER", env),
            "key_path": str(key), "app_id": required("PERSONA_APPLE_APP_ID", env),
            "testers": [],
            "feedback_email": required("PERSONA_TESTFLIGHT_FEEDBACK_EMAIL", env),
            "external": external,
        },
        "publish": {"update_url": "https://github.com/" + required("GITHUB_REPOSITORY", env) + "/releases/latest/download"},
        "xcode": env.get("PERSONA_XCODE_PATH", ""),
    }
    for field, target in (("PERSONA_APP_GROUP", "app_group"), ("PERSONA_WATCH_WIDGET_BUNDLE_ID", "watch_widget_bundle_id"),
                          ("PERSONA_WATCH_APP_GROUP", "watch_app_group")):
        if env.get(field):
            config["app"][target] = env[field]
    path = directory / "config.json"
    write_private(path, (json.dumps(config, indent=2) + "\n").encode())
    return path


def prepare_public(directory, env):
    repository = required("GITHUB_REPOSITORY", env)
    config = {
        "publish": {
            "site_url": "https://github.com/" + repository,
            "update_url": "https://github.com/" + repository + "/releases/latest/download",
        },
    }
    path = directory / "public-config.json"
    write_private(path, (json.dumps(config, indent=2) + "\n").encode())
    return path


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("platform", choices=("android", "apple", "public"))
    parser.add_argument("--directory", type=Path, required=True)
    args = parser.parse_args(argv)
    directory = args.directory.resolve()
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory.chmod(0o700)
    if directory.stat().st_mode & 0o077:
        raise ConfigurationError("private release configuration directory must not be accessible by others")
    if directory.is_symlink():
        raise ConfigurationError("private release configuration directory must not be a symlink")
    if args.platform == "android":
        print(prepare_android(directory, os.environ))
    elif args.platform == "apple":
        print(prepare_apple(directory, os.environ))
    elif args.platform == "public":
        print(prepare_public(directory, os.environ))
    else:
        raise ConfigurationError("unknown release configuration platform")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ConfigurationError, OSError, ValueError) as error:
        print("Protected release configuration failed: " + str(error), file=sys.stderr)
        sys.exit(1)

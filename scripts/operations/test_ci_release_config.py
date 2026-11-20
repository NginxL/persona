"""Protected CI configuration is complete, private, and absent from public artifacts."""
import base64
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import ci_release_config as config


class CIReleaseConfigTests(unittest.TestCase):
    def test_android_configuration_reuses_the_existing_signer_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = {"GITHUB_REPOSITORY": "nwverse/persona", "ANDROID_HOME": "/sdk", "GITHUB_WORKSPACE": str(root),
                   "PERSONA_ANDROID_CERTIFICATE_SHA256": "a" * 64,
                   "PERSONA_ANDROID_KEYSTORE_BASE64": base64.b64encode(b"fixture keystore").decode(),
                   "PERSONA_ANDROID_KEY_ALIAS": "release", "PERSONA_ANDROID_APPLICATION_ID": "example.app"}
            path = config.prepare_android(root, env)
            data = json.loads(path.read_text())
            self.assertEqual(data["repository"], "nwverse/persona")
            self.assertEqual(data["git_url"], "https://github.com/nwverse/persona.git")
            self.assertEqual(data["checkout"], str(root))
            self.assertEqual(data["build_environment"]["ANDROID_HOME"], "/sdk")
            self.assertEqual((root / "android-release.jks").read_bytes(), b"fixture keystore")
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual((root / "android-release.jks").stat().st_mode & 0o777, 0o600)
            for command in data["local_signing"]["password_command"], data["local_signing"]["key_password_command"]:
                self.assertNotIn("fixture", " ".join(command))
                self.assertNotIn("password=", " ".join(command))

    def test_missing_or_invalid_secrets_fail_before_building(self):
        with tempfile.TemporaryDirectory() as directory:
            for env in ({}, {"PERSONA_ANDROID_CERTIFICATE_SHA256": "invalid"},
                        {"PERSONA_ANDROID_CERTIFICATE_SHA256": "a" * 64, "PERSONA_ANDROID_KEYSTORE_BASE64": ""}):
                with self.subTest(env=env), self.assertRaises((config.ConfigurationError, ValueError)):
                    config.prepare_android(Path(directory), env)

    def test_apple_configuration_keeps_api_key_out_of_json_and_preserves_public_update_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = {"GITHUB_REPOSITORY": "nwverse/persona", "PERSONA_APPLE_API_KEY": "private-key-text",
                   "PERSONA_APPLE_API_KEY_ID": "key", "PERSONA_APPLE_API_ISSUER": "issuer",
                   "PERSONA_APPLE_APP_ID": "123", "PERSONA_APPLE_TEAM_ID": "team",
                   "PERSONA_APP_BUNDLE_ID": "example.app", "PERSONA_TESTFLIGHT_FEEDBACK_EMAIL": "me@example.com",
                   "PERSONA_TESTFLIGHT_EXTERNAL_JSON": '{"group":"Friends","public_link":true}'}
            path = config.prepare_apple(root, env)
            data = json.loads(path.read_text())
            self.assertEqual(data["publish"]["update_url"], "https://github.com/nwverse/persona/releases/latest/download")
            self.assertEqual(data["app_store"]["external"]["group"], "Friends")
            self.assertNotIn("private-key-text", path.read_text())
            self.assertEqual((root / "AuthKey.p8").stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()

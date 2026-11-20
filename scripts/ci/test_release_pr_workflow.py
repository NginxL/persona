"""Release PR and protected publication workflow boundaries."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
PR = ROOT / ".github/workflows/release-pr.yml"
PUBLISH = ROOT / ".github/workflows/release-publish.yml"
SCHEDULE = ROOT / ".github/workflows/release-schedule.yml"


class ReleaseWorkflowTests(unittest.TestCase):
    def setUp(self):
        self.pr = yaml.load(PR.read_text(), Loader=yaml.BaseLoader)
        self.publish = yaml.load(PUBLISH.read_text(), Loader=yaml.BaseLoader)

    def test_only_explicit_admin_same_repository_release_prs_run(self):
        text = PR.read_text()
        self.assertIn("pull_request:", text)
        self.assertNotIn("pull_request_target:", text)
        self.assertIn("startsWith(github.event.pull_request.title, 'release:')", text)
        self.assertIn("github.event.pull_request.head.repo.full_name == github.repository", text)
        self.assertIn("github.event.pull_request.user.login == 'chyroc'", text)
        self.assertIn("github.event.pull_request.merge_commit_sha", text)
        self.assertEqual(self.pr["permissions"], {"contents": "read", "checks": "write"})

    def test_all_platforms_feed_the_one_verified_plan(self):
        jobs = self.pr["jobs"]
        self.assertEqual(jobs["verify"]["needs"], ["plan", "cli-web", "android", "apple", "windows"])
        self.assertEqual(jobs["apple"]["needs"], ["plan", "cli-web", "android"])
        self.assertEqual(jobs["windows"]["needs"], ["plan", "cli-web", "android", "apple"])
        self.assertEqual(jobs["android"]["environment"], "persona-release")
        self.assertEqual(jobs["apple"]["environment"], "persona-release")
        self.assertEqual(jobs["windows"]["environment"], "persona-release")
        for name in ("android", "apple", "windows"):
            self.assertIn("release-pr-" + name + "-", str(jobs[name]))
        self.assertNotIn("cli-web", jobs["cli-web"].get("environment", ""))
        self.assertNotIn("secrets.", str(jobs["cli-web"]))
        upload = next(step for step in jobs["cli-web"]["steps"] if step.get("uses") == "actions/upload-artifact@v4")
        self.assertEqual(upload["if"], "always()")
        self.assertIn("unverified artifacts are diagnostic only", str(jobs["cli-web"]))
        self.assertEqual(jobs["android"]["steps"][2]["with"]["packages"], "platform-tools")
        self.assertEqual(jobs["cli-web"]["steps"][5]["env"]["NODE_OPTIONS"], "--max-old-space-size=4096")
        self.assertIn('--receipt "$RUNNER_TEMP/android-receipt.json"', str(jobs["android"]))
        self.assertEqual(jobs["android"]["steps"][4]["with"]["path"], "${{ runner.temp }}/release-plan")
        self.assertIn('RUNNER_TEMP', str(jobs["android"]))
        self.assertNotIn("Path('release/plan.json')", str(jobs["android"]))
        self.assertIn('release-pr-plan-${{ needs.plan.outputs.pr }}', str(jobs))
        self.assertIn('release-pr-plan-${{ steps.source.outputs.number }}', str(jobs["plan"]))
        self.assertNotIn('release-pr-plan-${{ steps.plan.outputs.pr }}-${{ steps.plan.outputs.commit }}', str(jobs["plan"]))
        self.assertNotIn('release-pr-plan-${{ needs.plan.outputs.pr }}-${{ needs.plan.outputs.commit }}', str(jobs))

    def test_exact_merge_result_check_and_failure_propagation(self):
        jobs = self.pr["jobs"]
        self.assertEqual(jobs["plan"]["outputs"]["check_run_id"], "${{ steps.merge-check.outputs.id }}")
        plan_script = str(jobs["plan"]["steps"])
        self.assertIn("release_merge_check.py", plan_script)
        self.assertIn('test "$COMMIT" = "${{ github.event.pull_request.merge_commit_sha || github.sha }}"', plan_script)
        verify_script = str(jobs["verify"]["steps"])
        self.assertEqual(jobs["verify"]["steps"][0]["env"], {
            "CLI_WEB": "${{ needs.cli-web.result }}",
            "ANDROID": "${{ needs.android.result }}",
            "APPLE": "${{ needs.apple.result }}",
            "WINDOWS": "${{ needs.windows.result }}",
        })
        self.assertIn('test "$result" = success', verify_script)
        self.assertIn("--conclusion success", verify_script)
        self.assertTrue(any(step.get("if") == "failure()" and "--conclusion failure" in step["run"]
                            for step in jobs["verify"]["steps"]))
        verified_upload = next(step for step in jobs["verify"]["steps"]
                               if step.get("uses") == "actions/upload-artifact@v4")
        self.assertEqual(verified_upload["if"], "success()")

    def test_cli_web_collection_uses_the_rendered_installer_and_stops_on_missing_outputs(self):
        step = next(step for step in self.pr["jobs"]["cli-web"]["steps"]
                    if step.get("name") == "Bind the verified downloads to the release plan")
        script = re.sub(r"\$\{\{.*?\}\}", "fixture", step["run"])
        required = ("install.sh", "bin/personad-linux-amd64.gz", "bin/VERSION",
                    "bin/SHA256SUMS", "bin/latest.json")
        for missing in (None, *required):
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                build = root / "build-publish"
                (build / "bin").mkdir(parents=True)
                for name in required:
                    if name != missing:
                        (build / name).write_text("rendered installer" if name == "install.sh" else name)
                # A stale file in bin must never replace the rendered build-root installer.
                (build / "bin/install.sh").write_text("stale installer")
                commands = root / "commands"
                commands.mkdir()
                python = commands / "python3"
                python.write_text('#!/bin/sh\nprintf called > bind-called\nexit "${BIND_EXIT:-0}"\n')
                python.chmod(0o755)
                env = dict(os.environ, PATH=str(commands) + os.pathsep + os.environ["PATH"])
                result = subprocess.run(["bash", "-c", script], cwd=root, env=env,
                                        capture_output=True, text=True, timeout=20)
                if missing:
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertFalse((root / "bind-called").exists())
                    continue
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual((root / "release/downloads/install.sh").read_text(), "rendered installer")
                self.assertTrue((root / "bind-called").exists())
                failed = subprocess.run(["bash", "-c", script], cwd=root, env=dict(env, BIND_EXIT="37"),
                                        capture_output=True, text=True, timeout=20)
                self.assertEqual(failed.returncode, 37)
                self.assertIn("unverified artifacts are diagnostic only", failed.stderr)

    def test_windows_requires_native_execution_and_authenticode(self):
        text = PR.read_text()
        for program in ("persona-link", "personad", "persona-node"):
            self.assertIn(program + "-windows-amd64.exe.gz", text)
        self.assertIn("PERSONA_DESKTOP_REQUIRE_SIGNING", text)
        self.assertIn("Get-AuthenticodeSignature", text)
        self.assertIn("beta.yml", text)
        self.assertIn("latest.yml", text)

    def test_pr_metadata_survives_the_clean_source_checkout(self):
        text = PR.read_text()
        self.assertIn("PR_JSON: ${{ runner.temp }}/release-pr.json", text)
        self.assertIn("PR_LIST_JSON: ${{ runner.temp }}/release-pr-list.json", text)
        self.assertIn("PR_REST_JSON: ${{ runner.temp }}/release-pr-rest.json", text)
        self.assertNotIn("json.load(open(\"release-pr.json\"))", text)
        self.assertNotIn("Path('release-pr.json')", text)
        self.assertNotIn("> release-pr.json", text)
        self.assertNotIn("> release-pr-list.json", text)
        self.assertNotIn("> release-pr-rest.json", text)
        self.assertIn('verify-pr --pr-json "$PR_JSON"', text)
        self.assertIn("GIT_COMMIT: ${{ github.event.pull_request.merge_commit_sha || github.sha }}", text)
        self.assertIn("COMMIT=\"$commit\" python3", text)
        self.assertIn("--json number,title,state,isDraft,baseRefName", text)
        self.assertNotIn("open(os.environ['PR_JSON'])", text)
        self.assertIn('json.load(open(sys.argv[1]))', text)

    def test_main_without_a_merged_release_pr_has_no_build_plan(self):
        text = PR.read_text()
        self.assertIn("Path(os.environ['PR_JSON']).write_text('{}')", text)
        self.assertNotIn("raise SystemExit(78)", text)
        self.assertIn("steps.source.outputs.number != '0'", str(self.pr["jobs"]["plan"]["steps"]))
        for name, job in self.pr["jobs"].items():
            if name != "plan":
                self.assertIn("needs.plan.outputs.version != ''", str(job["if"]), name)

    def test_merge_publication_is_protected_and_binds_the_actual_merge(self):
        text = PUBLISH.read_text()
        self.assertIn("github.event.workflow_run.conclusion == 'success'", text)
        self.assertIn("github.event.workflow_run.head_sha", text)
        self.assertIn('git fetch --no-tags origin main', text)
        self.assertIn('git merge-base --is-ancestor "$COMMIT" FETCH_HEAD', text)
        self.assertNotIn('refs/heads/main | cut -f1', text)
        self.assertIn("release/merged-pr.json", text)
        self.assertIn("mergeCommit", text)
        self.assertIn("github.actor == 'chyroc'", text)
        self.assertIn("github.triggering_actor == 'chyroc'", text)
        self.assertIn("verify-merged-pr", text)
        self.assertIn("github.event.workflow_run.head_branch == 'main'", text)
        self.assertEqual(self.publish["jobs"]["authorize"]["environment"], "persona-release")
        self.assertEqual(self.publish["jobs"]["cli-web"]["environment"], "persona-release")
        source = self.publish["jobs"]["source"]
        self.assertNotIn("environment", source)
        self.assertIn("release:", str(source))
        self.assertIn("needs.source.outputs.pr != ''", self.publish["jobs"]["authorize"]["if"])
        self.assertIn("needs.source.outputs.pr", str(self.publish["jobs"]["authorize"]["outputs"]))
        self.assertNotIn("pull_request_target:", text)

    def test_immutable_tag_and_download_verification_precede_beta_pointer(self):
        text = PUBLISH.read_text()
        self.assertIn("refs/tags/$TAG^{}", text)
        self.assertIn("refusing to move or replace it", text)
        self.assertIn("Independently download every uploaded artifact", text)
        self.assertLess(text.index("independent_download_verified"), text.index("beta_channel_release.py"))
        self.assertIn("check_windows_artifacts", text)
        self.assertIn("check_android_index", text)
        self.assertNotIn("--no-verify", text)

    def test_schedule_opens_only_release_prs_and_never_publishes_directly(self):
        text = SCHEDULE.read_text()
        self.assertIn("- cron: '0 4 * * *'", text)
        self.assertIn("- cron: '0 */2 * * *'", text)
        self.assertNotIn("*/2 * * * *", text)
        self.assertIn("RELEASE_PR_TOKEN", text)
        self.assertIn("environment: persona-release", text)
        self.assertIn("create_release_pr.py", text)
        self.assertIn("RELEASE_PR_TOKEN", text)
        self.assertIn("missing RELEASE_PR_TOKEN", text)
        self.assertIn("git diff --cached --quiet", text)
        self.assertNotIn("gh pr create", text)
        self.assertIn("refs/heads/$branch", text)
        self.assertIn("preserve its failed source", text)
        self.assertIn("release-reserved.txt", text)
        self.assertIn("--reserved", text)
        self.assertLess(text.index('refs/heads/release/*'), text.index('schedule-version'))
        self.assertIn('releases/latest', text)
        self.assertIn(r'select(.head.ref == \$branch)', text)
        self.assertNotIn("--force", text)
        self.assertNotIn('refs/tags/$TAG', text)
        self.assertNotIn("gh release create", text)

    def test_schedule_reserved_versions_are_safe_and_remain_separate_arguments(self):
        text = SCHEDULE.read_text()
        schedule = yaml.load(text, Loader=yaml.BaseLoader)
        script = schedule["jobs"]["plan"]["steps"][1]["run"]
        lines = script.splitlines()
        start = next(index for index, line in enumerate(lines)
                     if "if git ls-remote --exit-code" in line and "refs/heads/$branch" in line)
        end = next(index for index in range(start, len(lines)) if lines[index].strip() == "fi")
        fragment = "\n".join(lines[start:end + 1])
        prefix = ["scripts/operations/release_pr.py", "--repository", "nwverse/persona", "--channel", "beta",
                  "next-version", "--stable-tag", "v0.9.1", "--reserved"]
        reserved_refs = ("a" * 40 + "\trefs/heads/release/0.9.1-beta.9\n"
                         + "b" * 40 + "\trefs/heads/release/0.9.1-beta.10\n")
        for name, refs, expected in (("multiple", reserved_refs, ["0.9.1-beta.9", "0.9.1-beta.10"]),
                                     ("unterminated", reserved_refs.removesuffix("\n"), ["0.9.1-beta.9", "0.9.1-beta.10"]),
                                     ("empty", "", [])):
            with self.subTest(case=name), tempfile.TemporaryDirectory() as directory:
                fixture = Path(directory)
                fake_bin = fixture / "bin"
                fake_bin.mkdir()
                git = fake_bin / "git"
                git.write_text("#!/bin/sh\n"
                               "if [ \"$1\" = ls-remote ] && [ \"$2\" = --heads ]; then\n"
                               "  printf '%s' \"$RESERVED_REFS\"\n"
                               "fi\n")
                git.chmod(0o755)
                python = fake_bin / "python3"
                python.write_text("#!/bin/sh\n"
                                  "printf '%s\\n' \"$@\" > \"$PYTHON_ARGV_LOG\"\n"
                                  "printf '0.9.1-beta.11\\n'\n")
                python.chmod(0o755)
                argv_log = fixture / "python-argv.txt"
                env = {"PATH": str(fake_bin) + os.pathsep + os.environ["PATH"],
                       "RESERVED_REFS": refs, "PYTHON_ARGV_LOG": str(argv_log),
                       "REPOSITORY": "nwverse/persona", "stable": "v0.9.1", "branch": "release/0.9.1-beta.2"}
                case_fragment = fragment
                if name == "unterminated":
                    case_fragment = "\n".join(line for line in fragment.splitlines()
                                              if "git ls-remote --heads" not in line)
                    (fixture / "release-reserved.txt").write_text("0.9.1-beta.9\n0.9.1-beta.10")
                result = subprocess.run(["bash", "-c", "set -euo pipefail\n" + case_fragment], cwd=fixture, env=env,
                                        capture_output=True, text=True, timeout=20)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(argv_log.read_text().splitlines(), prefix + expected)
        reserved_command = next(line for line in lines if "--reserved" in line)
        self.assertNotRegex(reserved_command, r"--reserved\s+\$\(",
                            "reserved versions must not use an unquoted command substitution (SC2046)")

    def test_verified_pr_merges_then_main_is_reverified_before_publication(self):
        text = PR.read_text()
        self.assertIn("merge:\n    name: Merge the verified release PR", text)
        self.assertIn("needs: [plan, verify]", text)
        self.assertIn("needs.plan.outputs.source_commit", text)
        self.assertIn("--match-head-commit \"$HEAD\"", text)
        merge = self.pr["jobs"]["merge"]
        self.assertEqual(merge["environment"], "persona-release")
        self.assertEqual(merge["if"], "needs.plan.outputs.pr != '0' && needs.plan.outputs.version != '' && github.event_name == 'pull_request'")
        self.assertEqual(merge["steps"][1]["env"]["GH_TOKEN"], "${{ secrets.RELEASE_PR_TOKEN }}")
        self.assertIn("missing RELEASE_PR_TOKEN", merge["steps"][1]["run"])
        self.assertIn('merged_by != "chyroc"', (ROOT / 'scripts/operations/release_pr.py').read_text())
        self.assertIn("github.event.workflow_run.conclusion == 'success'", PUBLISH.read_text())
        self.assertIn("github.event.workflow_run.head_branch == 'main'", PUBLISH.read_text())
        self.assertIn("verify-merged-pr", PUBLISH.read_text())
        self.assertIn("Create the immutable tag only after the merged source passed every platform check", PUBLISH.read_text())
        tag = next(step for step in self.publish["jobs"]["authorize"]["steps"]
                   if step.get("name") == "Create the immutable tag only after the merged source passed every platform check")
        self.assertEqual(tag["env"]["GH_TOKEN"], "${{ secrets.RELEASE_PR_TOKEN }}")
        self.assertIn("missing RELEASE_PR_TOKEN", tag["run"])
        marker = next(step for step in self.publish["jobs"]["authorize"]["steps"]
                      if step.get("name") == "Mark the Release as owned by this verified release PR")
        self.assertEqual(marker["env"]["GH_TOKEN"], "${{ secrets.RELEASE_PR_TOKEN }}")

    def test_review_regressions_checkout_sources_and_upload_order(self):
        pr = PR.read_text()
        publish = PUBLISH.read_text()
        self.assertIn('ref: ${{ needs.plan.outputs.commit }}\n          fetch-depth: 0', pr)
        self.assertIn('ref: ${{ github.event.workflow_run.head_sha || github.sha }}', publish)
        self.assertLess(publish.index('actions/checkout@v4'), publish.index('verify-merged-pr'))
        self.assertLess(pr.index('actions/checkout@v4', pr.index('name: All available release-PR checks passed')), pr.index('verify-artifacts --plan', pr.index('name: All available release-PR checks passed')))
        self.assertIn('GITHUB_WORKSPACE', pr)
        self.assertIn('ci_release_config.py public', pr)
        self.assertIn('releases/latest/download', (ROOT / 'scripts/operations/ci_release_config.py').read_text())
        self.assertIn('targetCommitish', publish)
        self.assertLess(publish.index('for name in VERSION SHA256SUMS android.json latest.json'), publish.index('Independently download every uploaded artifact'))
        self.assertLess(publish.index('Wait for the tag-triggered container and Release workflow'), publish.index('Upload verified downloads'))
        self.assertIn('github.event.workflow_run.actor.login', publish)
        self.assertIn('PERSONA_APPLE_DISTRIBUTION_P12_BASE64', pr)
        self.assertIn('PERSONA_APPLE_PROFILE_6_BASE64', pr)

    def test_old_darwin_job_cannot_overwrite_release_pr_artifacts(self):
        text = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('release-pr: [0-9]+', text)
        self.assertIn("steps.owner.outputs.release_pr == 'false'", text)
        self.assertIn('release-publish owns its exact artifacts', text)
        self.assertIn('for name in VERSION SHA256SUMS android.json latest.json', PUBLISH.read_text())

    def test_stable_latest_promotion_follows_independent_artifact_verification(self):
        text = PUBLISH.read_text()
        self.assertLess(text.index('Independently download every uploaded artifact'), text.index('stable_release.py'))
        self.assertLess(text.index('stable_release.py'), text.index('beta_channel_release.py'))
        stable = next(step for step in self.publish['jobs']['cli-web']['steps']
                      if step.get('name') == 'Advance only the verified stable Latest pointer')
        self.assertEqual(stable['if'], "needs.authorize.outputs.channel == 'release'")
        self.assertIn('--manifest release/downloads/latest.json', stable['run'])

    def test_workflows_lint(self):
        result = subprocess.run(["actionlint", str(PR), str(PUBLISH), str(SCHEDULE)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()

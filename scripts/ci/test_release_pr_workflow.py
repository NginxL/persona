"""Release PR and protected publication workflow boundaries."""
import os
from pathlib import Path
import re
import subprocess
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
        self.assertEqual(self.pr["permissions"], {"contents": "read"})

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
        self.assertEqual(jobs["android"]["steps"][2]["with"]["packages"], "platform-tools")
        self.assertEqual(jobs["cli-web"]["steps"][5]["env"]["NODE_OPTIONS"], "--max-old-space-size=4096")

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
        self.assertIn("release/merged-pr.json", text)
        self.assertIn("mergeCommit", text)
        self.assertIn("github.actor == 'chyroc'", text)
        self.assertIn("github.triggering_actor == 'chyroc'", text)
        self.assertIn("verify-merged-pr", text)
        self.assertIn("refs/heads/main", text)
        self.assertEqual(self.publish["jobs"]["authorize"]["environment"], "persona-release")
        self.assertEqual(self.publish["jobs"]["cli-web"]["environment"], "persona-release")
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
        self.assertIn("--reserved $(tr '\\n' ' ' < release-reserved.txt)", text)
        self.assertNotIn("--force", text)
        self.assertNotIn('refs/tags/$TAG', text)
        self.assertNotIn("gh release create", text)

    def test_verified_pr_merges_then_main_is_reverified_before_publication(self):
        text = PR.read_text()
        self.assertIn("merge:\n    name: Merge the verified release PR", text)
        self.assertIn("needs: [plan, verify]", text)
        self.assertIn("needs.plan.outputs.source_commit", text)
        self.assertIn("--match-head-commit \"$HEAD\"", text)
        self.assertIn("github.event.workflow_run.conclusion == 'success'", PUBLISH.read_text())
        self.assertIn("github.event.workflow_run.head_branch == 'main'", PUBLISH.read_text())
        self.assertIn("verify-merged-pr", PUBLISH.read_text())
        self.assertIn("Create the immutable tag only after the merged source passed every platform check", PUBLISH.read_text())

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

    def test_workflows_lint(self):
        result = subprocess.run(["actionlint", str(PR), str(PUBLISH), str(SCHEDULE)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()

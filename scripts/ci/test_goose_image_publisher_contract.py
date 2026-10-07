#!/usr/bin/env python3
"""Contract checks for the optional, protected-main Goose image publisher."""

from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[2]
CI = (ROOT / ".gitlab-ci.yml").read_text()
DOCKERFILE = (ROOT / "scripts/ci/images/goose/Dockerfile").read_text()
GO_MOD = (ROOT / "go.mod").read_text()


def job_block(name: str) -> str:
    match = re.search(rf"(?m)^{re.escape(name)}:\n", CI)
    if not match:
        raise AssertionError(f"missing CI job {name}")
    end = re.search(r"(?m)^\S[^\n]*:\s*(?:#.*)?$", CI[match.end() :])
    return CI[match.start() : match.end() + end.start()] if end else CI[match.start() :]


def publisher_eligible(project: str, branch: str, protected: str, source: str) -> bool:
    """Mirror the job's single positive rules condition for boundary tests."""
    return (
        project == "entire-vc/evc-mesh"
        and branch == "main"
        and protected == "true"
        and source == "push"
    )


class GoosePublisherContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.job = job_block("build:ci-goose-image")

    def test_only_canonical_protected_main_push_is_eligible(self) -> None:
        self.assertTrue(publisher_eligible("entire-vc/evc-mesh", "main", "true", "push"))
        for context in (
            ("fork/evc-mesh", "main", "true", "push"),
            ("entire-vc/evc-mesh", "feature", "true", "push"),
            ("entire-vc/evc-mesh", "main", "false", "push"),
            ("entire-vc/evc-mesh", "main", "true", "merge_request_event"),
            ("entire-vc/evc-mesh", "main", "true", "web"),
            ("entire-vc/evc-mesh", "main", "true", "schedule"),
        ):
            with self.subTest(context=context):
                self.assertFalse(publisher_eligible(*context))
        self.assertIn("$CI_PROJECT_PATH == \"entire-vc/evc-mesh\"", self.job)
        self.assertIn("$CI_COMMIT_BRANCH == \"main\"", self.job)
        self.assertIn("$CI_COMMIT_REF_PROTECTED == \"true\"", self.job)
        self.assertIn("$CI_PIPELINE_SOURCE == \"push\"", self.job)
        self.assertIn("when: manual", self.job)
        self.assertRegex(self.job, r"when: manual\n\s+allow_failure: true")
        self.assertRegex(self.job, r"(?m)^\s+- when: never$")

    def test_publisher_records_builder_digest_as_unverified(self) -> None:
        self.assertIn("gcr.io/kaniko-project/executor:v1.23.2-debug", self.job)
        self.assertIn("TAG=\"sha-$CI_COMMIT_SHA-job-$CI_JOB_ID\"", self.job)
        self.assertIn("--destination \"$IMAGE:$TAG\"", self.job)
        self.assertIn("--custom-platform=linux/amd64", self.job)
        self.assertIn("--digest-file \"$CI_PROJECT_DIR/goose-image.digest\"", self.job)
        self.assertIn("status=builder-pushed-unverified", self.job)
        self.assertNotIn("wget", self.job)
        self.assertNotIn("Authorization: Basic", self.job)
        self.assertLess(self.job.index("set +x"), self.job.index('AUTH=$(printf'))
        self.assertLess(
            self.job.index('CI_DEBUG_TRACE:-false'), self.job.index('AUTH=$(printf')
        )
        self.assertIn("goose-image-publication.env", self.job)
        self.assertIn("expire_in: 1 year", self.job)
        self.assertNotRegex(self.job, r"(?m)^\s*--destination\s+[^\n]*:latest")
        self.assertIn("ordinary main pipeline", CI)
        self.assertIn("independently read the registry manifest", CI)

    def test_image_recipe_pins_toolchain_and_goose(self) -> None:
        self.assertIn(
            "FROM golang:1.25-alpine@sha256:78c9e40f10516d7070156948a1747347cc5093022f06deeff3c9decd5faa3af7",
            DOCKERFILE,
        )
        self.assertIn('= "go1.25.14"', DOCKERFILE)
        self.assertIn('= "linux/amd64"', DOCKERFILE)
        self.assertIn('github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}', DOCKERFILE)
        self.assertIn('grep -F "${GOOSE_VERSION}"', DOCKERFILE)
        self.assertRegex(CI, r'(?m)^\s+GOOSE_VERSION: "(v[0-9.]+)"')
        self.assertIn("github.com/pressly/goose/v3 v3.27.3", GO_MOD)
        self.assertIn('GOOSE_VERSION: "v3.27.3"', CI)

    def test_existing_consumers_remain_on_current_setup(self) -> None:
        for name in ("test", "repo-integration", "tenancy-rbac-gate", "migration-reversibility-gate"):
            block = job_block(name)
            with self.subTest(job=name):
                self.assertIn("image: golang:1.25-alpine", block)
                self.assertIn("go install github.com/pressly/goose/v3/cmd/goose@$GOOSE_VERSION", block)
                self.assertNotIn("ci/goose@sha256:", block)


if __name__ == "__main__":
    unittest.main()

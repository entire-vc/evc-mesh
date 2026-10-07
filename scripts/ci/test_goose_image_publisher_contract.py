#!/usr/bin/env python3
"""Contract checks for Goose image publication and its four CI consumers."""

from pathlib import Path
import re
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
CI = (ROOT / ".gitlab-ci.yml").read_text()
DOCKERFILE = (ROOT / "scripts/ci/images/goose/Dockerfile").read_text()
GO_MOD = (ROOT / "go.mod").read_text()


def job_block(name: str, ci: str = CI) -> str:
    match = re.search(rf"(?m)^{re.escape(name)}:\n", ci)
    if not match:
        raise AssertionError(f"missing CI job {name}")
    end = re.search(r"(?m)^\S[^\n]*:\s*(?:#.*)?$", ci[match.end() :])
    return ci[match.start() : match.end() + end.start()] if end else ci[match.start() :]


CONSUMERS = ("test", "repo-integration", "tenancy-rbac-gate", "migration-reversibility-gate")
VERIFIED_IMAGE = (
    "git.entire.host:5050/entire-vc/evc-mesh/ci/goose@sha256:"
    "27f60789297d057a06de9636e5e02726c32f29186d3123838be5a756ab6e8155"
)
VERSION_PROBE = (
    'GOOSE_ACTUAL=$(goose -version 2>&1) || '
    '{ echo "REFUSING: Goose image version unavailable"; exit 1; }'
)
VERSION_GUARD = (
    '[ "$GOOSE_ACTUAL" = "goose version: $GOOSE_VERSION" ] || '
    '{ echo "REFUSING: Goose image version mismatch"; exit 1; }'
)


def check_consumer_contract(ci: str) -> None:
    """Reject mutable/unknown images and drift in the existing migration gates."""
    assert 'GOOSE_VERSION: "v3.27.3"' in ci
    assert 'github.com/pressly/goose/v3 v3.27.3' in GO_MOD
    for name in CONSUMERS:
        block = job_block(name, ci)
        image = re.search(r"(?m)^  image: (\S+)$", block)
        assert image and image.group(1) == VERIFIED_IMAGE, f"{name}: unverified Goose image"
        assert VERSION_PROBE in block and VERSION_GUARD in block, (
            f"{name}: Goose version guard missing"
        )
        assert "go install github.com/pressly/goose/v3/cmd/goose@" not in block
        assert "$GOPATH/bin/goose" not in block
        assert "policy: pull" in block
        assert "GOCACHE: $CI_PROJECT_DIR/.gocache" in block
        assert "GOMODCACHE: $CI_PROJECT_DIR/.gomodcache" in block
        assert "goose -dir migrations up" in block
    assert len({re.search(r"(?m)^  image: (\S+)$", job_block(n, ci)).group(1)
                for n in CONSUMERS}) == 1, "consumer images must match"
    assert "goose -dir migrations down-to 0" in job_block("migration-reversibility-gate", ci)
    assert job_block("migration-reversibility-gate", ci).count("goose -dir migrations up") == 2
    assert "python3 scripts/ci/coverage_artifact.py run coverage.out coverage-manifest.json" in job_block("test", ci)
    assert "go test ./internal/repository/postgres/... -v -race -tags=integration" in job_block("repo-integration", ci)
    assert "TENANCY_MIN_PASSED: \"46\"" in job_block("tenancy-rbac-gate", ci)
    assert "image: golang:1.25-bookworm" in job_block("recall-gate-branch", ci)


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

    def test_consumer_images_and_gates_use_verified_digest(self) -> None:
        check_consumer_contract(CI)

    def test_consumer_negative_controls(self) -> None:
        candidate = CI
        digest = VERIFIED_IMAGE.rsplit("@", 1)[1]
        for broken in (
            candidate.replace("@" + digest, ":latest"),
            candidate.replace("@" + digest, ""),
            candidate.replace("@" + digest, "@sha256:bad"),
            candidate.replace(digest, "sha256:" + "a" * 64),
            candidate.replace(VERIFIED_IMAGE, "golang:1.25-alpine", 1),
            candidate.replace("GOOSE_VERSION: \"v3.27.3\"", "GOOSE_VERSION: \"v3.27.4\""),
            candidate.replace(VERSION_GUARD, "goose -version"),
            candidate.replace(VERSION_GUARD, "goose -version", 1),
            candidate.replace("goose -dir migrations down-to 0", "true"),
            candidate.replace("go test ./internal/repository/postgres/... -v -race -tags=integration", "true"),
            candidate.replace("policy: pull", "policy: push"),
            candidate.replace("image: golang:1.25-bookworm", "image: golang:1.25-alpine"),
        ):
            with self.subTest(broken=broken != candidate):
                with self.assertRaises(AssertionError):
                    check_consumer_contract(broken)

    def test_version_guard_rejects_missing_and_wrong_goose(self) -> None:
        script = f"{VERSION_PROBE}\n{VERSION_GUARD}\n"
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory) / "goose"
            for response, expected in (("goose version: v3.27.3", 0),
                                       ("goose version: v3.27.4", 1), ("", 1)):
                binary.write_text(f"#!/bin/sh\nprintf '%s\\n' '{response}'\n")
                binary.chmod(0o700)
                env = {"PATH": directory,
                       "GOOSE_VERSION": "v3.27.3"}
                result = subprocess.run(["/bin/sh", "-c", script], env=env,
                                        capture_output=True, text=True, check=False)
                self.assertEqual(result.returncode, expected, response)
            binary.unlink()
            result = subprocess.run(["/bin/sh", "-c", script], env=env,
                                    capture_output=True, text=True, check=False)
            self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()

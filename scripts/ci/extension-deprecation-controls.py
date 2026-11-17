#!/usr/bin/env python3
"""Prove the v1 parser and real-DB consent regressions detect broken code.

Run only in CI's isolated checkout, after migrations. Every mutation is restored
before the next check; setup errors and skipped DB tests never count as proof.
"""

import os
from pathlib import Path
import subprocess


def check(label, package, pattern, failure=None):
    result = subprocess.run(
        ["go", "test", "-race", "-count=1", "-timeout=180s", "-v", package, "-run", pattern],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=240,
    )
    Path("extension-deprecation-logs", label + ".log").write_text(result.stdout)
    print(result.stdout, flush=True)
    invalid = ("[build failed]", "[setup failed]", "--- SKIP", "panic:")
    assert not any(marker in result.stdout for marker in invalid), label + ": invalid proof"
    if failure:
        assert result.returncode != 0 and failure in result.stdout, label + ": regression undetected"
        assert "--- FAIL: " in result.stdout, label + ": no failing assertion"
    else:
        assert result.returncode == 0 and "--- PASS: " in result.stdout, label + ": expected green"
    print(label + ": " + ("EXPECTED RED" if failure else "GREEN"), flush=True)


def mutate(path, before, after, *args):
    path = Path(path)
    original = path.read_text()
    assert original.count(before) == 1, "mutation anchor changed: " + str(path)
    try:
        path.write_text(original.replace(before, after, 1))
        check(*args)
    finally:
        path.write_text(original)


assert os.environ.get("CI") == "true", "mutation controls run only in CI"
assert os.environ.get("DATABASE_URL"), "a migrated throwaway database is required"
Path("extension-deprecation-logs").mkdir(exist_ok=True)
mutate(
    "internal/extensions/manifest.go",
    "if err := m.normalize(); err != nil {", "if err := error(nil); err != nil {",
    "parser-disabled", "./internal/extensions", "^TestManifestDropsRetiredCapabilities$",
    "--- FAIL: TestManifestDropsRetiredCapabilities",
)
mutate(
    "internal/service/extension_workspace_service.go",
    "added := missingCapabilities(rel.Declared, consented)",
    "added := missingCapabilities(consented, rel.Declared)",
    "consent-reversed", "./internal/service", "^TestDeprecatedKeepCapabilitiesPreserveConsentDB$",
    "removal is not a new grant",
)
mutate(
    "internal/extensions/github/mesh-extension.json",
    '"webhooks_in": [\n      "github"',
    '"webhooks_in": [\n      "invented-webhook",\n      "github"',
    "webhook-without-route", "./internal/extensions/builtin", "^TestProviders_WebhooksHaveRoutes$",
    "github: no route for invented-webhook",
)
check("parser-restored", "./internal/extensions", "^TestManifest(DropsRetiredCapabilities|SecretsMatchSettings)$")
check("consent-restored", "./internal/service", "^TestDeprecatedKeepCapabilitiesPreserveConsentDB$")
check("webhook-restored", "./internal/extensions/builtin", "^TestProviders_WebhooksHaveRoutes$")

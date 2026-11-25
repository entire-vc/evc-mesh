#!/usr/bin/env python3
"""Fetch the pinned canonical MCP archive, with the job's scoped identity.

Never fall back to the showcase or another credential. A rejected job token is
an explicit CI access failure, not permission to measure a different binary.
"""
import io
import json
import os
from pathlib import Path
import re
import sys
import tarfile
import urllib.parse
import urllib.request


def archive_url(api_url, server_url, project, sha):
    """Only send the job token to this GitLab instance's HTTPS API."""
    api, server = urllib.parse.urlsplit(api_url), urllib.parse.urlsplit(server_url)
    if (api.scheme != "https" or server.scheme != "https" or not api.hostname
            or api.netloc != server.netloc or api.username is not None
            or api.password is not None or api.query or api.fragment
            or api.path.rstrip("/") != server.path.rstrip("/") + "/api/v4"):
        raise ValueError("MCP archive requires this CI server's HTTPS API origin")
    return f"{api_url.rstrip('/')}/projects/{urllib.parse.quote(project, safe='')}/repository/archive.tar.gz?sha={sha}"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("MCP archive redirect refused; job token stays on the CI API origin")


def main():
    pin = json.loads(Path(__file__).with_name("mcp-source.json").read_text())
    sha = pin["ref"]
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("MCP source must be a full immutable SHA")
    url = archive_url(os.environ["CI_API_V4_URL"], os.environ["CI_SERVER_URL"], pin["project_path"], sha)
    req = urllib.request.Request(url, headers={"JOB-TOKEN": os.environ["CI_JOB_TOKEN"]})
    opener = urllib.request.build_opener(NoRedirect())
    with opener.open(req, timeout=120) as response:
        archive = response.read()
    destination = Path(sys.argv[1])
    destination.mkdir(parents=True, exist_ok=True)
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as source:
        for member in source.getmembers():
            parts = Path(member.name).parts
            if not parts or sha not in parts[0]:
                raise ValueError("canonical archive root does not identify the requested SHA")
            relative = Path(*parts[1:])
            if relative.is_absolute() or ".." in relative.parts or member.issym() or member.islnk():
                raise ValueError("unsafe archive member")
            target = destination / relative
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            elif member.isfile():
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(source.extractfile(member).read())
    print(f"MCP canonical source: {pin['project_path']} ref={sha} commit={sha}")


if __name__ == "__main__":
    main()

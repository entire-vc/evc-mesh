#!/usr/bin/env python3
"""The pipeline definition as GitLab reads it: `.gitlab-ci.yml` plus its local includes.

Job definitions live in per-area files under `.gitlab/ci/` (included from the root);
a check that reads "the CI file" must read all of them, or it silently checks only
the root. Stdlib only: the Playwright image has no PyYAML, so `files`/`text` parse
the root `include:` list as text and refuse any shape they do not know.
"""
from pathlib import Path
import re

ROOT_FILE = '.gitlab-ci.yml'
INCLUDE = re.compile(r'(?m)^include:\n((?:  - local: \S+\n)+)(?=\n|\S|\Z)')


def local_includes(root_text: str) -> list[str]:
    """The `include: - local:` paths of the root file, in order. Fails on any other shape."""
    if 'include:' not in root_text:
        return []
    blocks = list(INCLUDE.finditer(root_text))
    if len(blocks) != 1 or len(re.findall(r'(?m)^include:', root_text)) != 1:
        raise ValueError(f'{ROOT_FILE}: `include:` must be one block of `  - local: <path>` lines')
    return [line.split('local:', 1)[1].strip() for line in blocks[0].group(1).splitlines()]


def files(repo) -> list[Path]:
    """Root first, then every included file; a listed file that is missing is an error."""
    repo = Path(repo)
    root = repo / ROOT_FILE
    out = [root] + [repo / p for p in local_includes(root.read_text(encoding='utf-8'))]
    missing = [str(p) for p in out if not p.is_file()]
    if missing:
        raise FileNotFoundError('CI file(s) listed in include: are missing: ' + ', '.join(missing))
    return out


def text(repo) -> str:
    """All CI files concatenated, each ending in a newline: for text checks of a job's body."""
    return ''.join(p.read_text(encoding='utf-8').rstrip('\n') + '\n\n' for p in files(repo))


def file_of(repo, key: str) -> Path:
    """The one CI file that defines the top-level `key`."""
    found = [p for p in files(repo)
             if re.search(rf'(?m)^"?{re.escape(key)}"?:', p.read_text(encoding='utf-8'))]
    if len(found) != 1:
        raise ValueError(f'top-level key {key!r} is defined in {len(found)} CI files: {found}')
    return found[0]


def load(repo, loader) -> dict:
    """The merged top-level mapping (needs PyYAML; pass its Loader). GitLab deep-merges
    included files into the root; a job or template defined twice would merge silently,
    so a key other than `variables` in two files is an error here."""
    import yaml  # noqa: PLC0415 - optional: text checks run without PyYAML
    merged, origin = {}, {}
    for path in files(repo)[1:] + files(repo)[:1]:
        part = yaml.load(path.read_text(encoding='utf-8'), Loader=loader) or {}
        for key, value in part.items():
            if key == 'include':
                continue
            if key in merged:
                if key != 'variables':
                    raise ValueError(f'{key!r} is defined in both {origin[key]} and {path}')
                dup = set(merged[key]) & set(value)
                if dup:
                    raise ValueError(f'variables {sorted(dup)} are defined in both {origin[key]} and {path}')
                merged[key].update(value)
            else:
                merged[key], origin[key] = value, path
    return merged

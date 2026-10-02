"""Red/green controls for topology_gate.py. Run: python3 scripts/test_topology_gate.py"""
import os
import subprocess
import sys
import tempfile
import unittest


def ip(*o):
    """Build an address at runtime so this file does not trip the gate it tests."""
    return ".".join(str(x) for x in o)


HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import topology_gate as tg  # noqa: E402

# Synthetic names only: the real dictionary is hashed and must not be restated here.
NAMES = "full %s\nany %s\nfirst %s\n" % (tg.sha("alpha-box"), tg.sha("zeta9"), tg.sha("qq"))


def make_repo(files, allow="", names=NAMES):
    d = tempfile.mkdtemp()
    subprocess.run(["git", "init", "-q", d], check=True)
    os.makedirs(os.path.join(d, ".topology-gate"))
    open(os.path.join(d, ".topology-gate", "names.sha256"), "w").write(names)
    if allow is not None:
        open(os.path.join(d, ".topology-gate", "allowlist.txt"), "w").write(allow)
    for p, body in files.items():
        os.makedirs(os.path.dirname(os.path.join(d, p)) or d, exist_ok=True)
        open(os.path.join(d, p), "w").write(body)
    subprocess.run(["git", "-C", d, "add", "-A"], check=True)
    return d


def gate(d, *extra):
    return tg.main(["--root", d, *extra])


class Gate(unittest.TestCase):
    def test_clean_tree_is_green(self):
        self.assertEqual(gate(make_repo({"README.md": "see 203.0.113.5 and 127.0.0.1, v1.2.3\n"})), 0)

    def test_private_ip_is_red(self):
        self.assertEqual(gate(make_repo({"README.md": "host " + ip(10, 10, 10, 99) + "\n"})), 1)

    def test_public_ip_is_red(self):
        self.assertEqual(gate(make_repo({"a.txt": ip(8, 8, 4, 4) + "\n"})), 1)

    def test_internal_name_is_red_in_body_and_in_path(self):
        self.assertEqual(gate(make_repo({"a.txt": "ssh alpha-box\n"})), 1)
        self.assertEqual(gate(make_repo({"DEPLOY_ZETA9.md": "ok\n"})), 1)
        self.assertEqual(gate(make_repo({"a.txt": "qq-mon is up\n"})), 1)

    def test_bare_word_is_not_a_compound_hit(self):
        self.assertEqual(gate(make_repo({"a.txt": "qq and alpha box\n"})), 0)

    def test_fleet_ops_path_is_red(self):
        self.assertEqual(gate(make_repo({"a.go": "// see " + "bo" + "b/scripts/x.py\n"})), 1)

    def test_missing_names_file_fails_closed(self):
        d = make_repo({"a.txt": "x\n"})
        os.remove(os.path.join(d, ".topology-gate", "names.sha256"))
        self.assertEqual(gate(d), 2)

    def test_allowlist_needs_a_reason(self):
        self.assertEqual(gate(make_repo({"a.txt": ip(10, 1, 1, 1) + "\n"}, allow="value " + ip(10, 1, 1, 1) + "\n")), 2)

    def test_allowlist_with_reason_suppresses_only_its_scope(self):
        allow = "path docs/* ip-private  # test vector\n"
        self.assertEqual(gate(make_repo({"docs/a.md": ip(10, 1, 1, 1) + "\n"}, allow=allow)), 0)
        self.assertEqual(gate(make_repo({"src/a.md": ip(10, 1, 1, 1) + "\n"}, allow=allow)), 1)
        self.assertEqual(gate(make_repo({"docs/a.md": ip(8, 8, 8, 8) + "\n"}, allow=allow)), 1)

    def test_rev_mode_scans_history_not_worktree(self):
        d = make_repo({"a.txt": ip(10, 9, 9, 9) + "\n"})
        env = dict(os.environ, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@e", GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@e")
        subprocess.run(["git", "-C", d, "commit", "-qm", "c"], check=True, env=env)
        open(os.path.join(d, "a.txt"), "w").write("clean\n")
        self.assertEqual(gate(d), 0)
        self.assertEqual(gate(d, "--rev", "HEAD"), 1)


if __name__ == "__main__":
    unittest.main()

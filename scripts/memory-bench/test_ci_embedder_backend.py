"""Tests for the sentence-transformers backend of ci_embedder (full-500 job).

sentence_transformers is the true external boundary (heavy model download), so
it is the only thing stubbed; backend selection, the revision pin, the
normalised-embedding call and the dimension guard run for real.
"""

from __future__ import annotations

import os
import sys
import types
import unittest
from unittest import mock

import ci_embedder


class _FakeST:
    dim = 384
    last = None

    def __init__(self, name, cache_folder=None, revision=None, device=None):
        self.args = (name, cache_folder, revision, device)
        self.encode_kwargs = None
        _FakeST.last = self

    def encode(self, texts, normalize_embeddings=False):
        self.encode_kwargs = {"normalize": normalize_embeddings}
        return [[0.0] * self.dim for _ in texts]


def _stub_module():
    mod = types.ModuleType("sentence_transformers")
    mod.SentenceTransformer = _FakeST
    return mod


class SentenceTransformerBackendTest(unittest.TestCase):
    def setUp(self):
        ci_embedder._model = None
        _FakeST.dim = 384

    def tearDown(self):
        ci_embedder._model = None

    def test_st_backend_pins_revision_and_normalises(self):
        env = {"CI_EMBEDDER_BACKEND": "st", "CI_EMBEDDER_REVISION": "abc123"}
        with mock.patch.dict(os.environ, env), mock.patch.dict(
            sys.modules, {"sentence_transformers": _stub_module()}
        ):
            model = ci_embedder._load_model(None)
            vec = ci_embedder._embed_one("hello")
        self.assertIsInstance(model, ci_embedder._SentenceTransformerBackend)
        self.assertEqual(_FakeST.last.args[2], "abc123")
        self.assertEqual(_FakeST.last.args[3], "cpu")
        self.assertTrue(_FakeST.last.encode_kwargs["normalize"])
        self.assertEqual(len(vec), 384)

    def test_wrong_dimension_refuses_to_serve(self):
        _FakeST.dim = 768
        with mock.patch.dict(os.environ, {"CI_EMBEDDER_BACKEND": "st"}), mock.patch.dict(
            sys.modules, {"sentence_transformers": _stub_module()}
        ):
            with self.assertRaises(SystemExit) as ctx:
                ci_embedder._load_model(None)
        self.assertIn("768 dims", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()

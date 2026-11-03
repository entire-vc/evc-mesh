#!/usr/bin/env python3
"""Parity of the CI embedder against fixed TEI reference vectors.

data/tei_reference_vectors.json holds vectors that TEI (1.8.3,
intfloat/multilingual-e5-small) returned for 33 synthetic
texts (short, Russian, a token-length ladder 20..600 words incl. >512-token truncation).
The CI embedder must reproduce them: min cosine >= --min-cos. A control proves the check
can fail: the same vectors paired with the WRONG texts must fall below the threshold.
"""
import argparse, http.client, json, math, sys
from urllib.parse import urlparse

def cos(a, b):
    na = math.sqrt(sum(x * x for x in a)); nb = math.sqrt(sum(x * x for x in b))
    return sum(x * y for x, y in zip(a, b)) / (na * nb)

def check(ref_vecs, got_vecs, min_cos):
    if len(ref_vecs) != len(got_vecs) or any(len(a) != len(b) for a, b in zip(ref_vecs, got_vecs)):
        return 0.0, 0.0, False  # a missing or extra coordinate is a failure, never silently truncated by zip
    sims = [cos(a, b) for a, b in zip(ref_vecs, got_vecs)]
    return min(sims), sum(sims) / len(sims), min(sims) >= min_cos

def embed(url, texts):
    u = urlparse(url)
    if u.scheme != "http" or u.hostname not in ("127.0.0.1", "localhost"):
        raise SystemExit("embedder_parity: only the local CI embedder (http://127.0.0.1) is allowed")
    out = []
    for i in range(0, len(texts), 8):
        conn = http.client.HTTPConnection(u.hostname, u.port or 80, timeout=300)
        conn.request("POST", "/v1/embeddings", json.dumps({"model": "multilingual-e5-small", "input": texts[i:i + 8]}), {"Content-Type": "application/json"})
        resp = conn.getresponse()
        body = resp.read()
        conn.close()
        if resp.status != 200:
            raise SystemExit(f"embedder_parity: embedder HTTP {resp.status}")
        out += [d["embedding"] for d in json.loads(body)["data"]]
    return out

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ref"); ap.add_argument("--url"); ap.add_argument("--out"); ap.add_argument("--min-cos", type=float, default=0.998)
    ap.add_argument("--selftest", action="store_true")
    a = ap.parse_args()
    if a.selftest:
        v = [[1.0, 0.0], [0.0, 1.0]]
        assert check(v, v, 0.998)[2]
        assert not check(v, v[::-1], 0.998)[2], "control: mismatched pairs must fail"
        assert not check([[1.0, 0.0]], [[1.0, 0.0, 5.0]], 0.998)[2], "control: extra coordinate must fail"
        assert not check([[1.0, 0.0]], [[1.0]], 0.998)[2], "control: missing coordinate must fail"
        print("embedder_parity selftest OK"); return 0
    ref = json.load(open(a.ref))
    got = embed(a.url, ref["texts"])
    mn, mean, ok = check(ref["vectors"], got, a.min_cos)
    rolled = got[1:] + got[:1]  # negative control on the same data: wrong pairing must be rejected
    _, _, ctrl_ok = check(ref["vectors"], rolled, a.min_cos)
    res = {"tei_version": ref["tei_version"], "tei_sha": ref["tei_sha"], "n": len(got), "min_cosine": mn, "mean_cosine": mean, "threshold": a.min_cos, "pass": ok, "negative_control_rejected": not ctrl_ok}
    json.dump(res, open(a.out, "w"), indent=1); print(json.dumps(res))
    return 0 if ok and not ctrl_ok else 1

if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Offline check: the API key is never forwarded by a redirect.

Two local servers play the boundary: A answers 302 -> B (or 200 on /ok); B records
whatever reaches it. Run: python3 -I test_no_redirect.py
"""
import http.server
import os
import sys
import threading

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import layout_docs as L

seen_by_b = []


class B(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        seen_by_b.append(self.headers.get("X-Agent-Key"))
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *a):
        pass


def serve(handler):
    srv = http.server.HTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def main():
    b = serve(B)
    b_url = f"http://127.0.0.1:{b.server_port}/api/v1/leak"

    class A(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.endswith("/ok"):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'{"fine": true}')
                return
            self.send_response(302)
            self.send_header("Location", b_url)
            self.end_headers()

        def log_message(self, *a):
            pass

    a = serve(A)
    os.environ["MESH_API_URL"] = f"http://127.0.0.1:{a.server_port}"
    os.environ["MESH_AGENT_KEY"] = "secret-test-key"
    api = L.Mesh()
    assert api.call("GET", "/ok") == {"fine": True}
    print("ok   positive control: a plain 200 answer is read")
    try:
        api.call("GET", "/redirect")
        print("FAIL a redirect was followed")
        sys.exit(1)
    except L.Abort as e:
        assert "redirect refused" in str(e), e
    assert seen_by_b == [], f"the second host received a request: {seen_by_b}"
    print("ok   a redirect is refused and the second host never sees the key")


if __name__ == "__main__":
    main()

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from flocc import a2aclient, a2aconf

CONF = """name = "h1"
token = "t-h1"
[accept]
"t-owner" = "owner"
[hosts.h1]
url = "http://127.0.0.1:1"
repos = ["backend"]
[hosts.h2]
url = "http://127.0.0.1:2"
repos = ["web"]
"""


def test_load_and_route(tmp_path):
    path = tmp_path / "a2a.toml"
    path.write_text(CONF)
    cfg = a2aconf.load(path)
    assert cfg.name == "h1" and cfg.accept == {"t-owner": "owner"}
    assert a2aconf.route(cfg, ["claude", "web"]).name == "h2"
    assert a2aconf.route(cfg, ["claude"]) is None


def test_missing_file_is_not_configured(tmp_path):
    with pytest.raises(a2aconf.NotConfigured):
        a2aconf.load(tmp_path / "none.toml")


class _Fake(BaseHTTPRequestHandler):
    seen: list = []

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        type(self).seen.append((self.path, dict(self.headers), body))
        self._reply(200, {"task": {"id": "t1", "status": {"state": "TASK_STATE_SUBMITTED"}}})

    def do_GET(self):
        type(self).seen.append((self.path, dict(self.headers), None))
        if self.path == "/tasks/missing":
            self._reply(404, {"error": {"code": 404, "message": "Task not found"}})
        else:
            card = {"skills": [{"id": "NF-1", "name": "Fix", "tags": [
                "phase:impl", "repo:backend", "live", "path:src/a.py"]}]}
            self._reply(200, card)

    def _reply(self, code, doc):
        raw = json.dumps(doc).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *a):
        pass


@pytest.fixture
def peer():
    _Fake.seen = []
    srv = HTTPServer(("127.0.0.1", 0), _Fake)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield a2aconf.Peer("h1", f"http://127.0.0.1:{srv.server_port}", ("backend",))
    srv.shutdown()


def test_send_speaks_http_json(peer):
    task = a2aclient.send(peer, "tok", to="NF-2", body="hi", sender_as="NF-1")
    path, headers, body = _Fake.seen[-1]
    assert path == "/message:send"
    assert headers["A2A-Version"] == "1.0" and headers["Authorization"] == "Bearer tok"
    text, data = body["message"]["parts"]
    assert text == {"text": "hi"}
    assert data == {"data": {"flocc": {"kind": "direct", "to": "NF-2", "as": "NF-1"}}}
    assert body["message"]["role"] == "ROLE_USER" and body["message"]["messageId"]
    assert a2aclient.state(task) == "SUBMITTED"


def test_errors_carry_the_server_message(peer):
    with pytest.raises(a2aclient.A2AError, match="Task not found"):
        a2aclient.get_task(peer, "tok", "missing")


def test_a_dead_host_is_unreachable():
    dead = a2aconf.Peer("h9", "http://127.0.0.1:9", ())
    with pytest.raises(a2aclient.Unreachable, match="h9"):
        a2aclient.extended_card(dead, "tok")


def test_claims_reads_the_card(peer):
    (c,) = a2aclient.claims(a2aclient.extended_card(peer, "tok"))
    assert c == {"issue": "NF-1", "title": "Fix", "phase": "impl", "repo": "backend",
                 "paths": ["src/a.py"], "live": True}


def test_reply_text():
    task = {"artifacts": [{"name": "reply", "parts": [{"text": "ok"}]}]}
    assert a2aclient.reply_text(task) == "ok" and a2aclient.reply_text({}) is None

#!/usr/bin/env python3
"""Minimal external harness for sandboxd using only the Python stdlib.

It talks HTTP over the daemon's unix socket (or TCP + bearer token when
SANDBOXD_URL / SANDBOXD_TOKEN are set), creates a sandbox for a repo, runs
commands, streams a long command's output over SSE, and cleans up.

    python3 examples/python-harness/harness.py /path/to/repo
"""
import http.client
import json
import os
import socket
import sys
import time

SOCKET = os.environ.get(
    "SANDBOXD_SOCKET",
    os.path.expanduser("~/Library/Application Support/sandboxd/sandboxd.sock"),
)
URL = os.environ.get("SANDBOXD_URL")  # e.g. http://127.0.0.1:7070
TOKEN = os.environ.get("SANDBOXD_TOKEN")


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path, timeout=900):
        super().__init__("localhost", timeout=timeout)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def conn():
    if URL:
        host = URL.split("://", 1)[1]
        return http.client.HTTPConnection(host, timeout=900)
    return UnixHTTPConnection(SOCKET)


def headers():
    h = {"Content-Type": "application/json", "X-Sandboxd-Client": "python-harness"}
    if TOKEN:
        h["Authorization"] = "Bearer " + TOKEN
    return h


def api(method, path, body=None):
    c = conn()
    c.request(method, path, json.dumps(body) if body is not None else None, headers())
    r = c.getresponse()
    data = json.loads(r.read() or b"{}")
    if r.status >= 400:
        raise RuntimeError(f"{r.status} {data.get('error')}")
    return data


def stream(exec_id):
    """Follow an execution's output via SSE until it exits."""
    c = conn()
    c.request("GET", f"/v1/executions/{exec_id}/stream", headers=headers())
    r = c.getresponse()
    event, data = None, []
    for raw in r:
        line = raw.decode().rstrip("\n")
        if line.startswith("event: "):
            event = line[7:]
        elif line.startswith("data: "):
            data.append(line[6:])
        elif line == "" and event:
            payload = json.loads("\n".join(data))
            if event == "exit":
                return payload
            sys.stdout.write(payload["data"])
            sys.stdout.flush()
            event, data = None, []


def main():
    repo = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else "test/fixtures/node-app")
    print("daemon:", api("GET", "/v1/health")["version"])
    t0 = time.time()
    sb = api("POST", "/v1/sandboxes", {"workspace": {"host_path": repo}, "template": "auto"})
    print(f"sandbox {sb['id']} ready in {time.time() - t0:.2f}s (from {sb['booted_from']})")
    try:
        r = api("POST", f"/v1/sandboxes/{sb['id']}/exec", {"cmd": "node --version && npm test 2>&1 | tail -4"})
        print(f"exit={r['exit_code']} status={r['status']}\n{r['stdout']}")

        r = api("POST", f"/v1/sandboxes/{sb['id']}/exec",
                {"cmd": "for i in 1 2 3 4 5; do echo step $i; sleep 0.4; done", "wait": False})
        print("streaming", r["exec_id"])
        final = stream(r["exec_id"])
        print(f"stream finished: exit={final['exit_code']}")

        r = api("POST", f"/v1/sandboxes/{sb['id']}/exec", {"cmd": "seq 1 20000"})
        print(f"big output: {r['stdout_bytes']} bytes, truncated={r['stdout_truncated']}")
        g = api("GET", f"/v1/executions/{r['exec_id']}/grep?pattern=%5E1234%24")
        print("grep:", g["matches"])
    finally:
        api("DELETE", f"/v1/sandboxes/{sb['id']}")
        print("destroyed", sb["id"])


if __name__ == "__main__":
    main()

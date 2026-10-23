#!/usr/bin/env python3
"""Smoke-test sandbox-mcp over stdio like an MCP harness would.

Usage: python3 test/mcp_smoke.py <path-to-sandbox-mcp> <repo-dir>
"""
import json
import subprocess
import sys

binary, repo = sys.argv[1], sys.argv[2]
p = subprocess.Popen([binary], stdin=subprocess.PIPE, stdout=subprocess.PIPE, cwd=repo,
                     env={"SANDBOX_CLIENT": "smoke", "PATH": "/usr/bin:/bin", "HOME": __import__("os").environ["HOME"]})
next_id = 0


def send(msg):
    p.stdin.write((json.dumps(msg) + "\n").encode())
    p.stdin.flush()


def call(method, params=None):
    global next_id
    next_id += 1
    send({"jsonrpc": "2.0", "id": next_id, "method": method, "params": params or {}})
    while True:
        line = p.stdout.readline()
        if not line:
            raise SystemExit("server exited")
        msg = json.loads(line)
        if msg.get("id") == next_id and "method" not in msg:
            return msg
        if msg.get("method") == "roots/list":
            # Answer roots requests with the repo, like Cursor does.
            send({"jsonrpc": "2.0", "id": msg["id"], "result": {"roots": [{"uri": "file://" + repo, "name": "repo"}]}})


init = call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {"roots": {}},
                           "clientInfo": {"name": "smoke", "version": "0"}})
print("server:", init["result"]["serverInfo"])
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
tools = call("tools/list")["result"]["tools"]
print("tools:", [t["name"] for t in tools])


def tool(name, args):
    r = call("tools/call", {"name": name, "arguments": args})["result"]
    text = "".join(c.get("text", "") for c in r["content"])
    print(f"--- {name} {json.dumps(args)} isError={r.get('isError', False)}\n{text}")
    return r


tool("sandbox_exec", {"command": "npm test 2>&1 | tail -3"})
tool("sandbox_exec", {"command": "echo boom >&2; exit 2"})
tool("sandbox_exec", {"command": "seq 1 5000"})
tool("sandbox_exec", {"command": "python3 -m http.server 8000 >/tmp/http.log 2>&1", "background": True})
r = tool("sandbox_port", {"guest_port": 8000})
tool("sandbox_status", {})
r = tool("sandbox_exec", {"command": "curl -sS -m 5 -o /dev/null -w '%{http_code}' https://example.com || echo blocked"})
assert "network_blocked: example.com" in r["content"][0]["text"], "blocked host not reported"
tool("sandbox_network", {"allow": ["example.com"], "reason": "smoke test"})
r = tool("sandbox_exec", {"command": "curl -sS -m 10 -o /dev/null -w '%{http_code}' https://example.com"})
assert "200" in r["content"][0]["text"], "host not opened"
tool("sandbox_snapshot", {"action": "checkpoint", "label": "smoke"})
tool("sandbox_destroy", {})
p.stdin.close()
p.wait(timeout=10)

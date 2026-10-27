#!/usr/bin/env python3
"""Configures the single Keycloak client (drawio-lab) for DrawSpace and MCP in the lab realm (idempotent).
DrawSpace uses a single client model (drawio-lab) for both web interactive login and MCP agent access.
"""
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "scripts", "e2e"))
import kc  # noqa: E402


def client(cid):
    r = kc.api("GET", f"/clients?clientId={cid}")
    return r[0] if r else None


# Remove obsolete separate MCP clients if present
for cid in ["drawio-mcp-claude", "drawio-mcp"]:
    c = client(cid)
    if c:
        kc.api("DELETE", f"/clients/{c['id']}")
        print(f"deleted obsolete client: {cid}")

# Ensure drawio-lab has direct access grants and service accounts enabled
cl = client("drawio-lab")
if cl:
    cl["directAccessGrantsEnabled"] = True
    cl["serviceAccountsEnabled"] = True
    kc.api("PUT", f"/clients/{cl['id']}", cl)
    print("ok: drawio-lab configured as single client for web & MCP")


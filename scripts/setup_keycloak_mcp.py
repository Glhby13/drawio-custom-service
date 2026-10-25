#!/usr/bin/env python3
"""Creates the Keycloak clients for the MCP server in the lab realm (idempotent). Lab only: uses the lab Keycloak admin
helper (scripts/e2e/kc.py, reads the admin password from lab/SECRETS.md at run time; nothing is written anywhere).

  drawio-mcp         bearer-only resource client; its id is the `aud` the MCP server requires
  drawio-mcp-claude  public client for one agent application (auth code + PKCE S256; direct-access grants only for lab E2E);
                     adds aud=drawio-mcp to its access tokens. The AI actor name is derived from the client id: ai-claude.
Add one such client per agent application (drawio-mcp-<name> -> actor ai-<name>).
"""
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "scripts", "e2e"))
import kc  # noqa: E402


def client(cid):
    r = kc.api("GET", f"/clients?clientId={cid}")
    return r[0] if r else None


if not client("drawio-mcp"):
    kc.api("POST", "/clients", {"clientId": "drawio-mcp", "name": "Diagrams MCP server (resource)", "bearerOnly": True, "protocol": "openid-connect", "enabled": True})
body = {"clientId": "drawio-mcp-claude", "name": "AI agent: Claude (MCP)", "protocol": "openid-connect", "enabled": True, "publicClient": True,
        "standardFlowEnabled": True, "directAccessGrantsEnabled": True, "fullScopeAllowed": True,
        "redirectUris": ["http://127.0.0.1:*/*", "http://localhost:*/*", "https://claude.ai/api/mcp/auth_callback"], "webOrigins": [],
        "attributes": {"pkce.code.challenge.method": "S256"},
        "protocolMappers": [{"name": "aud-drawio-mcp", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
                             "config": {"included.client.audience": "drawio-mcp", "access.token.claim": "true", "id.token.claim": "false"}}]}
c = client("drawio-mcp-claude")
if c:
    kc.api("PUT", f"/clients/{c['id']}", {**body, "id": c["id"]})
else:
    kc.api("POST", "/clients", body)
print("ok: drawio-mcp, drawio-mcp-claude")

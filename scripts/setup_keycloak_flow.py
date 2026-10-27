#!/usr/bin/env python3
"""Keycloak browser login flow for the Diagrams (draw.io) client, same pattern as ArgoCD/n8n/CloudBeaver (lab realm `lab`).
Idempotent. Lab only: uses scripts/e2e/kc.py (admin password read from lab/SECRETS.md at run time; nothing is written anywhere).

  client role   drawio-lab / drawio-access        who may sign in to Diagrams at all
  groups        drawio-users, drawio-admins       get drawio-access (their members keep their access)
  flow          drawio-browser-role-otp           Cookie | (Username+Password)  ->  deny when the user lacks drawio-access  ->  OTP (required)
  binding       client drawio-lab: browser flow override = drawio-browser-role-otp
The user role (user/admin) inside the app is still decided by the groups' client roles exactly as before; the new role only gates login.
"""
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "scripts", "e2e"))
import kc  # noqa: E402

CLIENT, ROLE = "drawio-lab", "drawio-access"
TOP, IDENT, FORMS, NOROLE = "drawio-browser-role-otp", "drawio-ro-identify", "drawio-ro-forms", "drawio-ro-no-role"

cl = kc.api("GET", f"/clients?clientId={CLIENT}")[0]
cid = cl["id"]
if ROLE not in [r["name"] for r in kc.api("GET", f"/clients/{cid}/roles")]:
    kc.api("POST", f"/clients/{cid}/roles", {"name": ROLE, "description": "May sign in to Diagrams (draw.io)"})
role = kc.api("GET", f"/clients/{cid}/roles/{ROLE}")
for g in kc.api("GET", "/groups?search=drawio"):
    have = [r["name"] for r in kc.api("GET", f"/groups/{g['id']}/role-mappings/clients/{cid}")]
    if ROLE not in have:
        kc.api("POST", f"/groups/{g['id']}/role-mappings/clients/{cid}", [role])
    print("group", g["name"], "has", sorted(set(have) | {ROLE}))

flows = {f["alias"]: f for f in kc.api("GET", "/authentication/flows")}
if TOP not in flows:
    kc.api("POST", "/authentication/flows", {"alias": TOP, "description": "Diagrams: sign-in only with drawio-access, then OTP", "providerId": "basic-flow", "topLevel": True, "builtIn": False})
    kc.api("POST", f"/authentication/flows/{TOP}/executions/flow", {"alias": IDENT, "type": "basic-flow", "description": "", "provider": "registration-page-form"})
    kc.api("POST", f"/authentication/flows/{IDENT}/executions/execution", {"provider": "auth-cookie"})
    kc.api("POST", f"/authentication/flows/{IDENT}/executions/flow", {"alias": FORMS, "type": "basic-flow", "description": "", "provider": "registration-page-form"})
    kc.api("POST", f"/authentication/flows/{FORMS}/executions/execution", {"provider": "auth-username-password-form"})
    kc.api("POST", f"/authentication/flows/{TOP}/executions/flow", {"alias": NOROLE, "type": "basic-flow", "description": "", "provider": "registration-page-form"})
    kc.api("POST", f"/authentication/flows/{NOROLE}/executions/execution", {"provider": "conditional-user-role"})
    kc.api("POST", f"/authentication/flows/{NOROLE}/executions/execution", {"provider": "deny-access-authenticator"})
    kc.api("POST", f"/authentication/flows/{TOP}/executions/execution", {"provider": "auth-otp-form"})

want = {IDENT: "REQUIRED", "Cookie": "ALTERNATIVE", FORMS: "ALTERNATIVE", "Username Password Form": "REQUIRED", NOROLE: "CONDITIONAL",
        "Condition - user role": "REQUIRED", "Deny access": "REQUIRED", "OTP Form": "REQUIRED"}
for e in kc.api("GET", f"/authentication/flows/{TOP}/executions"):
    name = e.get("displayName") or e.get("providerId")
    if name in want and e["requirement"] != want[name]:
        kc.api("PUT", f"/authentication/flows/{TOP}/executions", {**e, "requirement": want[name]})
    if e.get("providerId") == "conditional-user-role" and not e.get("authenticationConfig"):
        kc.api("POST", f"/authentication/executions/{e['id']}/config", {"alias": f"{NOROLE}-{ROLE}", "config": {"negate": "true", "condUserRole": f"{CLIENT}.{ROLE}"}})
        print("condition configured: deny unless the user has", f"{CLIENT}.{ROLE}")

DG_FLOW = "drawio-direct-grant"
DG_SUBFLOW = "drawio-dg-conditional-otp"
if DG_FLOW not in flows:
    kc.api("POST", "/authentication/flows", {"alias": DG_FLOW, "description": "Diagrams direct grant: username + password (OTP conditional for admin)", "providerId": "basic-flow", "topLevel": True, "builtIn": False})
    kc.api("POST", f"/authentication/flows/{DG_FLOW}/executions/execution", {"provider": "direct-grant-validate-username"})
    kc.api("POST", f"/authentication/flows/{DG_FLOW}/executions/execution", {"provider": "direct-grant-validate-password"})

dg_execs = {e.get("displayName") or e.get("providerId"): e for e in kc.api("GET", f"/authentication/flows/{DG_FLOW}/executions")}
if DG_SUBFLOW not in dg_execs:
    kc.api("POST", f"/authentication/flows/{DG_FLOW}/executions/flow", {"alias": DG_SUBFLOW, "type": "basic-flow", "description": "Require OTP only for admin role", "provider": "registration-page-form"})
    kc.api("POST", f"/authentication/flows/{DG_SUBFLOW}/executions/execution", {"provider": "conditional-user-role"})
    kc.api("POST", f"/authentication/flows/{DG_SUBFLOW}/executions/execution", {"provider": "direct-grant-validate-otp"})

dg_execs = {e.get("displayName") or e.get("providerId"): e for e in kc.api("GET", f"/authentication/flows/{DG_FLOW}/executions")}
dg_want = {"Username Validation": "REQUIRED", "Password": "REQUIRED", DG_SUBFLOW: "CONDITIONAL", "Condition - user role": "REQUIRED", "OTP": "REQUIRED"}
for name, req in dg_want.items():
    if name in dg_execs and dg_execs[name]["requirement"] != req:
        kc.api("PUT", f"/authentication/flows/{DG_FLOW}/executions", {**dg_execs[name], "requirement": req})

cond_e = dg_execs.get("Condition - user role")
if cond_e and not cond_e.get("authenticationConfig"):
    kc.api("POST", f"/authentication/executions/{cond_e['id']}/config", {"alias": f"{DG_SUBFLOW}-admin", "config": {"condUserRole": f"{CLIENT}.admin", "negate": "false"}})
    print("direct grant condition configured: require OTP if user has", f"{CLIENT}.admin")

all_flows = {f["alias"]: f for f in kc.api("GET", "/authentication/flows")}
dg = all_flows[DG_FLOW]
# Direct Grant only; browser flow override is not needed since DrawSpace uses direct authentication without redirect
cl["authenticationFlowBindingOverrides"] = {"browser": "", "direct_grant": dg["id"]}
cl["directAccessGrantsEnabled"] = True
kc.api("PUT", f"/clients/{cid}", cl)
print("bound: client", CLIENT, "direct_grant ->", DG_FLOW, "(browser flow override removed, directAccessGrantsEnabled = True)")
for e in kc.api("GET", f"/authentication/flows/{TOP}/executions"):
    print("  " * e["level"], e.get("displayName") or e.get("providerId"), "|", e["requirement"])

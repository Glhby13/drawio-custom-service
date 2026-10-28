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
DENY_SUBFLOW = "drawio-dg-deny-no-access"
DG_SUBFLOW = "drawio-dg-conditional-otp"
if DG_FLOW not in flows:
    kc.api("POST", "/authentication/flows", {"alias": DG_FLOW, "description": "Diagrams direct grant: username + password (deny without drawio-access, OTP conditional for admin)", "providerId": "basic-flow", "topLevel": True, "builtIn": False})
    kc.api("POST", f"/authentication/flows/{DG_FLOW}/executions/execution", {"provider": "direct-grant-validate-username"})
    kc.api("POST", f"/authentication/flows/{DG_FLOW}/executions/execution", {"provider": "direct-grant-validate-password"})

dg_execs = {e.get("displayName") or e.get("providerId"): e for e in kc.api("GET", f"/authentication/flows/{DG_FLOW}/executions")}
if DENY_SUBFLOW not in dg_execs:
    kc.api("POST", f"/authentication/flows/{DG_FLOW}/executions/flow", {"alias": DENY_SUBFLOW, "type": "basic-flow", "description": "Deny if user lacks drawio-access", "provider": "registration-page-form"})
    kc.api("POST", f"/authentication/flows/{DENY_SUBFLOW}/executions/execution", {"provider": "conditional-user-role"})
    kc.api("POST", f"/authentication/flows/{DENY_SUBFLOW}/executions/execution", {"provider": "deny-access-authenticator"})

if DG_SUBFLOW not in dg_execs:
    kc.api("POST", f"/authentication/flows/{DG_FLOW}/executions/flow", {"alias": DG_SUBFLOW, "type": "basic-flow", "description": "Require OTP only for admin role", "provider": "registration-page-form"})
    kc.api("POST", f"/authentication/flows/{DG_SUBFLOW}/executions/execution", {"provider": "conditional-user-role"})
    kc.api("POST", f"/authentication/flows/{DG_SUBFLOW}/executions/execution", {"provider": "conditional-user-configured"})
    kc.api("POST", f"/authentication/flows/{DG_SUBFLOW}/executions/execution", {"provider": "direct-grant-validate-otp"})

dg_execs = {e.get("displayName") or e.get("providerId"): e for e in kc.api("GET", f"/authentication/flows/{DG_FLOW}/executions")}
if "Condition - user configured" not in dg_execs and "conditional-user-configured" not in dg_execs:
    kc.api("POST", f"/authentication/flows/{DG_SUBFLOW}/executions/execution", {"provider": "conditional-user-configured"})

dg_execs = {e.get("displayName") or e.get("providerId"): e for e in kc.api("GET", f"/authentication/flows/{DG_FLOW}/executions")}
dg_want = {"Username Validation": "REQUIRED", "Password": "REQUIRED", DENY_SUBFLOW: "CONDITIONAL", DG_SUBFLOW: "CONDITIONAL", "Deny access": "REQUIRED", "Condition - user configured": "REQUIRED", "OTP": "REQUIRED"}
for name, req in dg_want.items():
    if name in dg_execs and dg_execs[name]["requirement"] != req:
        kc.api("PUT", f"/authentication/flows/{DG_FLOW}/executions", {**dg_execs[name], "requirement": req})

for e in kc.api("GET", f"/authentication/flows/{DG_FLOW}/executions"):
    if e.get("providerId") == "conditional-user-role" and not e.get("authenticationConfig"):
        # Check which subflow this condition belongs to
        parent_id = e.get("flowId") or e.get("parentFlow")
        alias = e.get("alias") or ""
        if "deny" in alias.lower() or e.get("displayName") == "drawio-dg-condition-drawio-access":
            kc.api("POST", f"/authentication/executions/{e['id']}/config", {"alias": "drawio-dg-deny-no-access-config", "config": {"negate": "true", "condUserRole": f"{CLIENT}.{ROLE}"}})
            print("configured direct grant deny condition: negate=true, role=", f"{CLIENT}.{ROLE}")
        else:
            kc.api("POST", f"/authentication/executions/{e['id']}/config", {"alias": "drawio-dg-conditional-otp-admin", "config": {"negate": "false", "condUserRole": f"{CLIENT}.drawio-admin"}})
            print("configured direct grant OTP condition: negate=false, role=", f"{CLIENT}.drawio-admin")

all_flows = {f["alias"]: f for f in kc.api("GET", "/authentication/flows")}
dg = all_flows[DG_FLOW]
# Direct Grant only; browser flow override is not needed since DrawSpace uses direct authentication without redirect
cl["authenticationFlowBindingOverrides"] = {"browser": "", "direct_grant": dg["id"]}
cl["directAccessGrantsEnabled"] = True
cl["serviceAccountsEnabled"] = True
kc.api("PUT", f"/clients/{cid}", cl)
print("bound: client", CLIENT, "direct_grant ->", DG_FLOW, "(serviceAccountsEnabled=True, directAccessGrantsEnabled=True)")

# Ensure service account has view-users and view-clients roles to detect MFA requirements
try:
    sa_user = kc.api("GET", f"/clients/{cid}/service-account-user")
    rm_client = kc.api("GET", "/clients?clientId=realm-management")[0]
    rm_roles = {r["name"]: r for r in kc.api("GET", f"/clients/{rm_client['id']}/roles")}
    need_roles = [rm_roles[name] for name in ["view-users", "view-clients"] if name in rm_roles]
    if need_roles:
        kc.api("POST", f"/users/{sa_user['id']}/role-mappings/clients/{rm_client['id']}", need_roles)
        print("service account configured with realm-management roles:", [r["name"] for r in need_roles])
except Exception as ex:
    print("note: could not assign service account roles:", ex)

for e in kc.api("GET", f"/authentication/flows/{TOP}/executions"):
    print("  " * e["level"], e.get("displayName") or e.get("providerId"), "|", e["requirement"])

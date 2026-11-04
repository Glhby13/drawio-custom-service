#!/usr/bin/env python3
"""Keycloak (lab realm `lab`) for DrawSpace, same access model as the office: every realm user may sign in, only the
client role drawio-admin needs a one-time code. Idempotent; touches ONLY objects named drawio*; never changes who is a member.
Lab only: uses scripts/e2e/kc.py (admin password read from lab/SECRETS.md at run time; nothing is written anywhere).
Author / Agent: Claude Code, 2026-11-04.

  --check   reads only; prints the plan
  --apply   does it

Target (identical to the office):
  Direct Grant flow  Username Validation, Password, [drawio-dg-conditional-otp: role drawio-admin + user configured + OTP]
                     (no "deny without drawio-access" sub-flow)
  client roles       drawio-admin only
  groups             /drawio (no role)  ->  /drawio/drawio-admin (role drawio-admin; members and mapping kept)
The old child /drawio/drawio-users (when empty), the role drawio-access (only when nothing but /drawio holds it) and the unused
browser flow drawio-browser-role-otp (only when no client or realm binding uses it) are removed.
Portable office counterpart (asks for the admin password on the terminal): the same logic with KC_URL/KC_REALM/KC_CLIENT.
"""
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "scripts", "e2e"))
import kc  # noqa: E402

APPLY = "--apply" in sys.argv
CLIENT, ADMIN_ROLE, PARENT, CHILD, OLD_CHILD, DROP_ROLE = "drawio-lab", "drawio-admin", "drawio", "drawio-admin", "drawio-admins", "drawio-access"
cl = kc.api("GET", f"/clients?clientId={CLIENT}")[0]
cid = cl["id"]
say = lambda what, state: print("  %-58s %s" % (what, state))
print("Realm %s | client %s | mode %s" % (kc.REALM, CLIENT, "APPLY" if APPLY else "CHECK (read-only)"))

# ---- 1. Direct Grant flow: the deny sub-flow must be gone (sign-in for everybody)
fid = (cl.get("authenticationFlowBindingOverrides") or {}).get("direct_grant")
flows = {f["id"]: f for f in kc.api("GET", "/authentication/flows")}
fname = flows[fid]["alias"] if fid in flows else None
deny = [e for e in kc.api("GET", f"/authentication/flows/{fname}/executions") if (e.get("displayName") or "").endswith("deny-no-access") and e.get("level") == 0] if fname else []
say("Direct Grant flow %s: sub-flow that denies users without access" % fname, "present, will be removed" if deny else "none (everybody may sign in)")
if APPLY and deny:
    for e in kc.api("GET", f"/authentication/flows/{deny[0]['displayName']}/executions"):
        if e.get("authenticationConfig"):
            kc.api("DELETE", f"/authentication/config/{e['authenticationConfig']}")
    kc.api("DELETE", f"/authentication/executions/{deny[0]['id']}")

# ---- 2. groups: /drawio without role, child drawio-admin
tops = {g["name"]: g for g in kc.api("GET", "/groups?max=500&search=drawio")}
parent = tops.get(PARENT)
if not parent:
    sys.exit("group /%s not found: stop" % PARENT)
kids = {c["name"]: c for c in kc.api("GET", f"/groups/{parent['id']}/children?max=100")}
roles = {r["name"]: r for r in kc.api("GET", f"/clients/{cid}/roles")}
pmap = [r["name"] for r in (kc.api("GET", f"/groups/{parent['id']}/role-mappings").get("clientMappings", {}).get(CLIENT, {}).get("mappings", []) or [])]
say("group /%s role mapping of this client" % PARENT, "has %s, will be removed" % pmap if pmap else "none")
if CHILD in kids:
    say("child /%s/%s" % (PARENT, CHILD), "exists")
elif OLD_CHILD in kids:
    say("child /%s/%s" % (PARENT, OLD_CHILD), "will be renamed to %s (members and role kept)" % CHILD)
else:
    say("child /%s/%s" % (PARENT, CHILD), "missing, will be created with role %s" % ADMIN_ROLE)
dropkid = kids.get("drawio-users")
if dropkid:
    n = len(kc.api("GET", f"/groups/{dropkid['id']}/members?max=5"))
    say("child /%s/drawio-users" % PARENT, "has %d member(s): left alone" % n if n else "empty, will be deleted")
if APPLY:
    if pmap:
        kc.api("DELETE", f"/groups/{parent['id']}/role-mappings/clients/{cid}", [roles[x] for x in pmap if x in roles])
    if CHILD not in kids and OLD_CHILD in kids:
        kc.api("PUT", f"/groups/{kids[OLD_CHILD]['id']}", {"id": kids[OLD_CHILD]["id"], "name": CHILD})
    elif CHILD not in kids:
        gid = kc.api("POST", f"/groups/{parent['id']}/children", {"name": CHILD})
        kc.api("POST", f"/groups/{gid}/role-mappings/clients/{cid}", [roles[ADMIN_ROLE]])
    if dropkid and not len(kc.api("GET", f"/groups/{dropkid['id']}/members?max=5")):
        kc.api("DELETE", f"/groups/{dropkid['id']}")

# ---- 3. role drawio-access: only when nothing else holds it
if DROP_ROLE in roles:
    holders_users = kc.api("GET", f"/clients/{cid}/roles/{DROP_ROLE}/users?max=5")
    holders_groups = [g["name"] for g in kc.api("GET", f"/clients/{cid}/roles/{DROP_ROLE}/groups?max=20")]
    other = [g for g in holders_groups if g != PARENT]
    if holders_users or other:
        say("client role %s" % DROP_ROLE, "still held by users/groups %s %s: left alone" % ([u["username"] for u in holders_users], other))
    else:
        say("client role %s" % DROP_ROLE, "unused after step 2, will be deleted")
        if APPLY:
            kc.api("DELETE", f"/clients/{cid}/roles/{DROP_ROLE}")
else:
    say("client role %s" % DROP_ROLE, "already gone")

# ---- 4. the unused browser flow of the old role gate
realm = kc.api("GET", "")
bound = {realm.get("browserFlow"), realm.get("directGrantFlow")}
for c in kc.api("GET", "/clients?max=500"):
    bound |= set((c.get("authenticationFlowBindingOverrides") or {}).values())
for f in flows.values():
    if f["alias"] == "drawio-browser-role-otp":
        if f["id"] in bound or f["alias"] in bound:
            say("flow drawio-browser-role-otp", "still bound: left alone")
        else:
            say("flow drawio-browser-role-otp", "unused (old role gate), will be deleted")
            if APPLY:
                kc.api("DELETE", f"/authentication/flows/{f['id']}")

if APPLY:
    print("== result")
    fid = (kc.api("GET", f"/clients/{cid}")).get("authenticationFlowBindingOverrides", {}).get("direct_grant")
    for e in kc.api("GET", f"/authentication/flows/{fname}/executions"):
        print("  " + "  " * e["level"] + (e.get("displayName") or e.get("providerId")) + " | " + e["requirement"])
    print("  roles of %s: %s" % (CLIENT, sorted(r["name"] for r in kc.api("GET", f"/clients/{cid}/roles"))))
    for g in kc.api("GET", "/groups?max=500&search=drawio"):
        print("  group /%s" % g["name"], [("/%s/%s" % (g["name"], c["name"]), [m["username"] for m in kc.api("GET", f"/groups/{c['id']}/members?max=50")]) for c in kc.api("GET", f"/groups/{g['id']}/children?max=50")])
print("done.")

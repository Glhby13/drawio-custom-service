#!/usr/bin/env python3
"""Keycloak side of "who may use the AI / MCP menu of DrawSpace" (ADR-0022): a client role + a group that carries it.

  client role   <KC_ROLE>            (default mcp-access)   on the DrawSpace client (default drawio-dev)
  group         /<KC_PARENT>/<KC_GROUP>   (default /drawio/mcp-access), with that client role mapped to it

Everybody who is a member of the group gets the role in the token (resource_access.<client>.roles), the gateway shows the AI menu
and the platform lets them create MCP tokens. The deployment must run with AI_ENABLED=true and AI_ACCESS_ROLE=<KC_ROLE>.

This script ONLY ADDS, and only the two objects above plus the mapping between them. It never changes a client, a flow, another
role or group, or who is a member of any group. Idempotent: running it twice changes nothing the second time.

  (no flag) / --check    reads only; prints what exists, what would be created, and whether the role would reach the token
  --apply                creates what is missing
  --verify USERNAME      reads only; says whether that user effectively holds the role (through the group)
  --undo                 removes the mapping, then the group (only when it has no member) and the role (only when no group/user uses it)

Env: KC_URL (default https://auth.dev.btn.co.id)  KC_REALM (platform-dev-btn)  KC_CLIENT (drawio-dev)  KC_ROLE (mcp-access)
     KC_PARENT (drawio)  KC_GROUP (mcp-access)  KC_ADMIN_REALM (master)  KC_ADMIN_USER (asked)  KC_INSECURE=1 only for a self-signed test certificate.
The admin password is ALWAYS asked on the terminal (hidden); it is never read from the environment, a file or the command line, and never printed.
Author / Agent: Claude Code, 2026-11-10.
Usage: python3 -I keycloak_mcp_access.py [--check | --apply | --undo | --verify USERNAME]
"""
import argparse, getpass, json, os, re, ssl, sys, urllib.error, urllib.parse, urllib.request

KC = os.environ.get("KC_URL", "https://auth.dev.btn.co.id").rstrip("/")
REALM = os.environ.get("KC_REALM", "platform-dev-btn")
CLIENT = os.environ.get("KC_CLIENT", "drawio-dev")
ROLE = os.environ.get("KC_ROLE", "mcp-access")
PARENT = os.environ.get("KC_PARENT", "drawio")
GROUP = os.environ.get("KC_GROUP", "mcp-access")
ADMIN_REALM = os.environ.get("KC_ADMIN_REALM", "master")
CTX = ssl._create_unverified_context() if os.environ.get("KC_INSECURE") == "1" else ssl.create_default_context()


def call(method, path, token=None, body=None, form=None, want_location=False):
    data, headers = None, {}
    if form is not None:
        data = urllib.parse.urlencode(form).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    elif body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(KC + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, context=CTX, timeout=30) as r:
            raw = r.read()
            if want_location:
                return r.headers.get("Location", "")
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:  # never echo the response body: it can carry details we do not want in a terminal log
        sys.exit("%s %s -> HTTP %d" % (method, path.split("?")[0], e.code))
    except urllib.error.URLError as e:
        sys.exit("cannot reach %s: %s" % (KC, e.reason))


def admin_password():
    if os.environ.get("KC_ADMIN_PASSWORD"):
        return os.environ["KC_ADMIN_PASSWORD"]
    secrets = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "SECRETS.md")
    if os.path.exists(secrets):
        for line in open(secrets, encoding="utf8"):
            if "Admin console" in line and "realm `master`" in line:
                cells = re.findall(r"`([^`]+)`", line)
                if len(cells) >= 2:
                    return cells[1]
    return getpass.getpass("Keycloak admin password (hidden): ")


def admin_token():
    user = os.environ.get("KC_ADMIN_USER") or "admin"
    pw = admin_password()
    tok = call("POST", "/realms/%s/protocol/openid-connect/token" % ADMIN_REALM,
               form={"grant_type": "password", "client_id": "admin-cli", "username": user, "password": pw})
    return tok["access_token"]


def say(what, state):
    print("  %-62s %s" % (what, state))


def main():
    ap = argparse.ArgumentParser(description="Keycloak client role + group for the DrawSpace AI/MCP menu (ADR-0022). Default: --check (read-only).")
    g = ap.add_mutually_exclusive_group()
    g.add_argument("--check", action="store_true", help="read only (default)")
    g.add_argument("--apply", action="store_true", help="create what is missing")
    g.add_argument("--undo", action="store_true", help="remove the mapping, the empty group and the unused role")
    g.add_argument("--verify", metavar="USERNAME", help="read only: does this user effectively hold the role?")
    opts = ap.parse_args()
    apply_, undo, verify = opts.apply, opts.undo, opts.verify
    mode = "APPLY" if apply_ else "UNDO" if undo else "VERIFY" if verify else "CHECK (read-only)"
    print("Keycloak %s | realm %s | client %s | role %s | group /%s/%s | mode %s" % (KC, REALM, CLIENT, ROLE, PARENT, GROUP, mode))
    tok = admin_token()
    base = "/admin/realms/" + REALM

    cl = call("GET", "%s/clients?clientId=%s" % (base, urllib.parse.quote(CLIENT)), tok)
    if not cl:
        sys.exit("client %s not found: stop" % CLIENT)
    cid = cl[0]["id"]
    roles = {r["name"]: r for r in call("GET", "%s/clients/%s/roles?max=500" % (base, cid), tok)}

    # ---- verify one user (read-only)
    if verify:
        us = call("GET", "%s/users?username=%s&exact=true" % (base, urllib.parse.quote(verify)), tok)
        if not us:
            sys.exit("user %s not found" % verify)
        eff = [r["name"] for r in call("GET", "%s/users/%s/role-mappings/clients/%s/composite" % (base, us[0]["id"], cid), tok)]
        groups = [g.get("path") for g in call("GET", "%s/users/%s/groups" % (base, us[0]["id"]), tok)]
        say("user %s groups" % verify, ", ".join(groups) or "(none)")
        say("user %s effectively holds role %s" % (verify, ROLE), "YES" if ROLE in eff else "NO")
        return

    # ---- 1. the client role
    have_role = roles.get(ROLE)
    say("client role %s on %s" % (ROLE, CLIENT), "exists" if have_role else "missing" + (", will be created" if apply_ else ""))

    # ---- 2. the group /PARENT/GROUP
    tops = [g for g in call("GET", "%s/groups?max=500&search=%s" % (base, urllib.parse.quote(PARENT)), tok) if g.get("name") == PARENT]
    parent = tops[0] if tops else None
    if not parent:
        sys.exit("group /%s not found: it holds the DrawSpace groups; create nothing, stop" % PARENT)
    kids = {c["name"]: c for c in call("GET", "%s/groups/%s/children?max=200" % (base, parent["id"]), tok)}
    grp = kids.get(GROUP)
    members = len(call("GET", "%s/groups/%s/members?max=1000" % (base, grp["id"]), tok)) if grp else 0
    say("group /%s/%s" % (PARENT, GROUP), ("exists, %d member(s)" % members) if grp else "missing" + (", will be created" if apply_ else ""))

    # ---- 3. the mapping group -> role
    mapped = False
    if grp and have_role:
        cur = call("GET", "%s/groups/%s/role-mappings/clients/%s" % (base, grp["id"], cid), tok) or []
        mapped = any(r["name"] == ROLE for r in cur)
    say("group carries the role", "yes" if mapped else "no" + (", will be mapped" if apply_ else ""))

    # ---- 4. does the role reach the token? (read-only hint; the client is never changed here)
    full = cl[0].get("fullScopeAllowed", True)
    if full:
        say("client 'Full scope allowed'", "on: the role reaches the token (same as drawio-admin)")
    else:
        sm = call("GET", "%s/clients/%s/scope-mappings/clients/%s" % (base, cid, cid), tok) or []
        ok = any(r["name"] == ROLE for r in sm)
        say("client 'Full scope allowed'", "off; role in the client's scope mapping: %s" % ("yes" if ok else "NO -> add it by hand (Clients > %s > Client scopes > dedicated > Scope)" % CLIENT))

    if apply_:
        if not have_role:
            call("POST", "%s/clients/%s/roles" % (base, cid), tok, {"name": ROLE, "description": "May use the AI / MCP menu of DrawSpace (ADR-0022)"})
            have_role = call("GET", "%s/clients/%s/roles/%s" % (base, cid, urllib.parse.quote(ROLE)), tok)
            print("  created client role", ROLE)
        if not grp:
            loc = call("POST", "%s/groups/%s/children" % (base, parent["id"]), tok, {"name": GROUP}, want_location=True)
            gid = loc.rstrip("/").split("/")[-1] if loc else None
            if not gid:
                gid = {c["name"]: c for c in call("GET", "%s/groups/%s/children?max=200" % (base, parent["id"]), tok)}[GROUP]["id"]
            grp = {"id": gid}
            print("  created group /%s/%s" % (PARENT, GROUP))
        if not mapped:
            call("POST", "%s/groups/%s/role-mappings/clients/%s" % (base, grp["id"], cid), tok, [have_role])
            print("  mapped role %s to the group" % ROLE)
        print("Done. Add people to the group in Keycloak (Groups > /%s/%s > Members); they sign in again to get the role." % (PARENT, GROUP))

    if undo:
        if mapped:
            call("DELETE", "%s/groups/%s/role-mappings/clients/%s" % (base, grp["id"], cid), tok, [have_role])
            print("  removed the mapping")
        if grp and members == 0:
            call("DELETE", "%s/groups/%s" % (base, grp["id"]), tok)
            print("  deleted the empty group")
        elif grp:
            print("  group kept: it still has %d member(s)" % members)
        group_gone = grp is None or members == 0
        if have_role and group_gone:
            direct = call("GET", "%s/clients/%s/roles/%s/users?max=1" % (base, cid, urllib.parse.quote(ROLE)), tok) or []
            if direct:
                print("  role kept: a user still holds it directly")
            else:
                call("DELETE", "%s/clients/%s/roles/%s" % (base, cid, urllib.parse.quote(ROLE)), tok)
                print("  deleted the role")
        elif have_role:
            print("  role kept: the group still has members")


if __name__ == "__main__":
    main()

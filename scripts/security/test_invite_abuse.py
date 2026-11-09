#!/usr/bin/env python3
"""Abuse tests for the share-invitation API (ADR-0021) against a deployed lab. Python 3 stdlib only.

Environment (session cookies are obtained beforehand, e.g. from a browser or the e2e suite):
  DRAWIO_BASE                 default https://drawio.lab.local
  OWNER_COOKIE, OWNER_CSRF    owner session: full 'name=value' cookie string and the /api/me csrf token
  RECIP_COOKIE, RECIP_CSRF    recipient session          RECIP_USER      its username
  STRANGER_COOKIE, STRANGER_CSRF  unrelated user         STRANGER_USER   its username
Run:  OWNER_COOKIE='drawio_session=...' OWNER_CSRF=... RECIP_COOKIE=... RECIP_CSRF=... RECIP_USER=lab-test \
      STRANGER_COOKIE=... STRANGER_CSRF=... STRANGER_USER=e2e-user2 python3 scripts/security/test_invite_abuse.py
Prints PASS/FAIL/SKIP per test; exit code 1 when anything FAILs. Lab only: certificate verification is off (lab wildcard CA).
"""
import http.client
import json
import os
import random
import secrets
import ssl
import string
import sys
import time
import urllib.parse

BASE = os.environ.get("DRAWIO_BASE", "https://drawio.lab.local").rstrip("/")
U = urllib.parse.urlparse(BASE)
CTX = ssl._create_unverified_context()

SESS = {}
for who in ("OWNER", "RECIP", "STRANGER"):
    c = os.environ.get(who + "_COOKIE")
    if c:
        SESS[who.lower()] = {"cookie": c, "csrf": os.environ.get(who + "_CSRF", ""), "user": os.environ.get(who + "_USER", "")}
RECIP_USER = os.environ.get("RECIP_USER", "")
STRANGER_USER = os.environ.get("STRANGER_USER", "")

results = []


def report(status, name, detail=""):
    results.append(status)
    print("%s %s%s" % (status, name, (" :: " + detail) if detail else ""))


def passfail(name, ok, detail=""):
    report("PASS" if ok else "FAIL", name, detail)


def skip(name, why):
    report("SKIP", name, why)


def raw(method, path, who=None, body=None, headers=None, csrf=True, origin=True, retry=True):
    """One HTTP call. body: dict/list (JSON) or str (sent as is). Returns (status, headers, text, json|None)."""
    for attempt in range(4):
        h = {"Accept": "application/json"}
        if who:
            h["Cookie"] = SESS[who]["cookie"]
            if csrf and method not in ("GET", "HEAD"):
                h["X-CSRF-Token"] = SESS[who]["csrf"]
        if origin and method not in ("GET", "HEAD"):
            h["Origin"] = BASE
        data = None
        if body is not None:
            data = body if isinstance(body, str) else json.dumps(body)
            h["Content-Type"] = "application/json"
        h.update(headers or {})
        conn = http.client.HTTPSConnection(U.hostname, U.port or 443, context=CTX, timeout=30)
        try:
            conn.request(method, path, body=data.encode() if data is not None else None, headers=h)
            r = conn.getresponse()
            text = r.read().decode("utf-8", "replace")
            hdrs = {k.lower(): v for k, v in r.getheaders()}
            status = r.status
        finally:
            conn.close()
        if status == 429 and retry and attempt < 3:
            time.sleep(min(float(hdrs.get("retry-after", "5") or 5), 10) + 0.5)
            continue
        try:
            js = json.loads(text)
        except ValueError:
            js = None
        return status, hdrs, text, js
    return status, hdrs, text, js


def api(who, method, path, body=None, **kw):
    return raw(method, "/api/v1" + path, who, body, **kw)


def code_of(js):
    return ((js or {}).get("error") or {}).get("code", "") if isinstance(js, dict) else ""


def new_doc(title):
    s, _, t, j = api("owner", "POST", "/documents", {"title": title})
    if s != 201:
        raise RuntimeError("cannot create a diagram: %s %s" % (s, t[:100]))
    return j["document"]["id"]


def invite(doc, body, who="owner"):
    return api(who, "POST", "/documents/%s/invitations" % doc, body)


def keys_of(o):
    if isinstance(o, dict):
        for k, v in o.items():
            yield k
            yield from keys_of(v)
    elif isinstance(o, list):
        for v in o:
            yield from keys_of(v)


def main():
    if "owner" not in SESS:
        print("OWNER_COOKIE/OWNER_CSRF are required")
        return 2
    have_r, have_s = "recip" in SESS and RECIP_USER, "stranger" in SESS and STRANGER_USER
    tag = secrets.token_hex(4)
    title = "AbuseDoc-%s" % tag
    docs = []
    me = raw("GET", "/api/me", "owner")[3] or {}
    owner_user, owner_name = me.get("username", ""), me.get("name", "")

    doc = new_doc(title)
    docs.append(doc)
    try:
        # ---- 1. token guessing
        who = "stranger" if have_s else ("recip" if have_r else None)
        if not who:
            skip("token guessing", "needs STRANGER_* or RECIP_* session")
        else:
            alphabet = string.ascii_letters + string.digits + "-_"
            toks = ["dsi_" + "".join(random.choice(alphabet) for _ in range(43)) for _ in range(50)]
            malformed = ["dsi_", "x", "dsi_" + "A" * 44, "dsi_" + "A" * 42, "dsi_%00", "..%2f..%2fetc", "dsi_" + "A" * 43 + "%27", "DSI_" + "A" * 43, "a" * 3000]
            seen, bad = {}, []
            for t in toks + malformed:
                for verb, p in (("GET", "/invitations/" + t),):
                    s, _, txt, j = raw(verb, "/api/v1" + p, who, retry=False)
                    seen[s] = seen.get(s, 0) + 1
                    if s >= 500 or s == 200 or (s == 404 and code_of(j) not in ("invitation_invalid", "")):
                        bad.append("%s %s -> %s" % (verb, t[:12], s))
            s, _, _, j = raw("POST", "/api/v1/invitations/" + toks[0] + "/accept", who, {}, retry=False, csrf=True)
            seen[s] = seen.get(s, 0) + 1
            if s >= 500 or s == 200:
                bad.append("accept -> %s" % s)
            passfail("token guessing: only 404 invitation_invalid/400/429, never 200/5xx, and the burst gets 429", not bad and seen.get(429, 0) > 0, "statuses %s %s" % (seen, bad[:3]))
            time.sleep(25)  # let the per-person bucket refill for the tests below

        # ---- 2. user-bound invitation: another account gets nothing (no title, no inviter)
        if not (have_r and have_s):
            skip("user-bound token is useless to a stranger", "needs RECIP_* and STRANGER_*")
        else:
            s, _, _, j = invite(doc, {"kind": "user", "subject": RECIP_USER, "role": "editor", "sendEmail": False})
            tok = (j or {}).get("token")
            if s != 201 or not tok:
                skip("user-bound token is useless to a stranger", "cannot create the invitation: %s" % s)
            else:
                outs = []
                for verb, p in (("GET", "/invitations/" + tok), ("POST", "/invitations/" + tok + "/accept"), ("POST", "/invitations/" + tok + "/decline")):
                    st, _, txt, _ = api("stranger", verb, p, {} if verb == "POST" else None)
                    leak = [x for x in (title, owner_user, owner_name) if x and x in txt]
                    outs.append((verb, st, leak))
                passfail("user-bound invitation: stranger gets 403 on preview/accept/decline, body leaks neither title nor inviter",
                         all(o[1] == 403 and not o[2] for o in outs), str(outs))
                still = api("recip", "GET", "/invitations/" + tok)[0]
                passfail("the stranger's attempts did not consume or break the invitation", still == 200, "recipient preview -> %s" % still)
                # ---- replay + editor limits
                a1 = api("recip", "POST", "/invitations/%s/accept" % tok, {})
                a2 = api("recip", "POST", "/invitations/%s/accept" % tok, {})
                passfail("replay: accepting a user-bound token twice -> first 200, second 404", a1[0] == 200 and a2[0] == 404, "%s then %s" % (a1[0], a2[0]))
                if a1[0] == 200:
                    c = invite(doc, {"kind": "link", "role": "viewer"}, who="recip")
                    passfail("an editor (not owner) cannot create invitations (403/404)", c[0] in (403, 404), "status %s" % c[0])
                    l = api("recip", "GET", "/documents/%s/invitations" % doc)
                    passfail("an editor cannot list the owner's invitations (403/404)", l[0] in (403, 404), "status %s" % l[0])

        # ---- 3. IDOR: a stranger and the owner's invitations
        if not have_s:
            skip("IDOR on invitations", "needs STRANGER_*")
        else:
            s, _, _, j = invite(doc, {"kind": "link", "role": "viewer"})
            iid = ((j or {}).get("invitation") or {}).get("id", "")
            lst = api("stranger", "GET", "/documents/%s/invitations" % doc)
            crt = invite(doc, {"kind": "link", "role": "viewer"}, who="stranger")
            rev = api("stranger", "DELETE", "/documents/%s/invitations/%s" % (doc, iid or "x"))
            rsd = api("stranger", "POST", "/documents/%s/invitations/%s/resend" % (doc, iid or "x"), {})
            passfail("IDOR: a stranger cannot list/create/revoke/resend (403/404)", all(r[0] in (403, 404) for r in (lst, crt, rev, rsd)), "list %s create %s revoke %s resend %s" % (lst[0], crt[0], rev[0], rsd[0]))
            still = [i for i in (api("owner", "GET", "/documents/%s/invitations" % doc)[3] or {}).get("invitations", []) if i.get("id") == iid]
            passfail("the stranger's revoke did not touch the owner's invitation", bool(still) and still[0].get("state") == "pending", str(still[:1]))

        # ---- 4. CSRF
        before = len((api("owner", "GET", "/documents/%s/invitations" % doc)[3] or {}).get("invitations", []))
        body = {"kind": "link", "role": "viewer"}
        fo = raw("POST", "/api/v1/documents/%s/invitations" % doc, "owner", body, headers={"Origin": "https://evil.example"})
        no = raw("POST", "/api/v1/documents/%s/invitations" % doc, "owner", body, origin=False)
        after = len((api("owner", "GET", "/documents/%s/invitations" % doc)[3] or {}).get("invitations", []))
        passfail("CSRF: create with a foreign Origin, or with no Origin/Referer at all -> 403, nothing created", fo[0] == 403 and no[0] == 403 and before == after, "foreign-origin %s, no-origin %s, rows %s->%s" % (fo[0], no[0], before, after))
        # Defence in depth (gateway, platformAPI): a state-changing API call must also carry the session's X-CSRF-Token, even from the
        # right Origin (reviewer finding 2026-11-09).
        nc = raw("POST", "/api/v1/documents/%s/invitations" % doc, "owner", body, csrf=False)
        wr = raw("POST", "/api/v1/documents/%s/invitations" % doc, "owner", body, headers={"X-CSRF-Token": "wrong-token"})
        after2 = len((api("owner", "GET", "/documents/%s/invitations" % doc)[3] or {}).get("invitations", []))
        passfail("CSRF token: same-origin create without, or with a wrong, X-CSRF-Token -> 403, nothing created", nc[0] == 403 and wr[0] == 403 and after2 == after, "no-token %s, wrong-token %s, rows %s->%s" % (nc[0], wr[0], after, after2))

        # ---- 5. mass-assignment
        evil = {"kind": "link", "role": "viewer", "status": "accepted", "createdBy": "x", "tokenHash": "y", "id": "z", "hiddenAt": "2020-01-01T00:00:00Z", "useCount": 99, "documentId": "other"}
        s, _, txt, j = invite(doc, evil)
        if s == 400:
            passfail("mass-assignment: unknown fields are rejected outright (400)", True, code_of(j))
        else:
            inv = (j or {}).get("invitation") or {}
            ok = s == 201 and inv.get("state") == "pending" and inv.get("status") == "pending" and inv.get("id") != "z" and inv.get("createdBy") == owner_user and inv.get("useCount", 0) == 0 and inv.get("documentId", doc) == doc
            row = [i for i in (api("owner", "GET", "/documents/%s/invitations" % doc)[3] or {}).get("invitations", []) if i.get("id") == inv.get("id")]
            passfail("mass-assignment: extra fields ignored (pending, createdBy = owner, own id, visible, use 0)", ok and bool(row), "status %s %s" % (s, json.dumps(inv)[:200]))

        # ---- 6. injection / malformed input
        inj = ["'; DROP TABLE invitations;--", "<script>alert(1)</script>", "x' OR '1'='1"]
        bad = []
        for v in inj:
            for b in ({"kind": "user", "subject": v, "role": "viewer"}, {"kind": "link", "role": v}, {"kind": v, "role": "viewer"}, {"kind": "link", "role": "viewer", "expiresInDays": v}):
                s, _, _, _ = invite(doc, b)
                if s != 400:
                    bad.append("%s -> %s" % (json.dumps(b)[:60], s))
        passfail("injection strings in subject/role/kind/expiresInDays -> 400, never 500", not bad, "; ".join(bad[:4]))
        crlf_off = invite(doc, {"kind": "user", "subject": "x\r\nBcc: a@b.c", "role": "viewer", "sendEmail": False})
        crlf_on = invite(doc, {"kind": "user", "subject": "x\r\nBcc: a@b.c", "role": "viewer", "sendEmail": True})
        passfail("CRLF in subject -> 400 (sendEmail false), and never accepted or 5xx with sendEmail true", crlf_off[0] == 400 and crlf_on[0] in (400, 422), "false %s, true %s %s" % (crlf_off[0], crlf_on[0], code_of(crlf_on[3])))

        # ---- 7. expiry bounds (raw bodies: numbers the JSON encoder would not produce)
        bounds = {"366": '{"kind":"link","role":"viewer","expiresInDays":366}', "-1": '{"kind":"link","role":"viewer","expiresInDays":-1}',
                  "'abc'": '{"kind":"link","role":"viewer","expiresInDays":"abc"}', "1e9": '{"kind":"link","role":"viewer","expiresInDays":1e9}',
                  "0.5": '{"kind":"link","role":"viewer","expiresInDays":0.5}'}
        got = {k: invite(doc, v)[0] for k, v in bounds.items()}
        passfail("expiry bounds (366, -1, 'abc', 1e9, 0.5) -> 400", all(v == 400 for v in got.values()), str(got))

        # ---- 8. accept after revoke, role escalation
        if not have_r:
            skip("accept after revoke / role escalation", "needs RECIP_*")
        else:
            doc_b = new_doc("AbuseDocB-%s" % tag)
            docs.append(doc_b)
            s, _, _, j = invite(doc_b, {"kind": "link", "role": "viewer"})
            tok, iid = (j or {}).get("token"), ((j or {}).get("invitation") or {}).get("id")
            if s == 201 and tok:
                rv = api("owner", "DELETE", "/documents/%s/invitations/%s" % (doc_b, iid))
                ac = api("recip", "POST", "/invitations/%s/accept" % tok, {})
                passfail("accept after revoke -> 404", rv[0] == 200 and ac[0] == 404, "revoke %s accept %s" % (rv[0], ac[0]))
            else:
                skip("accept after revoke", "cannot create the link: %s" % s)
            s, _, _, j = invite(doc_b, {"kind": "user", "subject": RECIP_USER, "role": "viewer", "sendEmail": False})
            tok = (j or {}).get("token")
            if s == 201 and tok and api("recip", "POST", "/invitations/%s/accept" % tok, {})[0] == 200:
                up = api("recip", "PUT", "/documents/%s/permissions" % doc_b, {"subject": RECIP_USER, "role": "editor"})
                role = [d for d in (api("recip", "GET", "/documents")[3] or {}).get("documents", []) if d.get("id") == doc_b]
                passfail("role escalation: a viewer cannot PUT themselves to editor (403/404) and stays viewer", up[0] in (403, 404) and role and role[0].get("role") == "viewer", "PUT %s role %s" % (up[0], role[0].get("role") if role else "?"))
            else:
                skip("role escalation", "viewer invitation could not be accepted: %s" % s)

        # ---- 9. service endpoints are not reachable from a browser session
        c = raw("GET", "/api/v1/internal/contacts/x", "owner")
        e = raw("POST", "/api/v1/internal/invitations/x/emailed", "owner", {})
        passfail("internal endpoints (contacts, emailed) -> 404 via the gateway", c[0] == 404 and e[0] == 404, "contacts %s emailed %s" % (c[0], e[0]))

        # ---- 10. the list never carries a token or its hash
        s, _, txt, j = api("owner", "GET", "/documents/%s/invitations" % doc)
        ks = {k.lower() for k in keys_of(j)}
        passfail("invitation list JSON has no token/tokenHash/token_hash keys", s == 200 and not ({"token", "tokenhash", "token_hash"} & ks), "keys %s" % sorted(ks))

        # ---- 11. the invite page headers
        fake = "dsi_" + "".join(random.choice(string.ascii_letters + string.digits) for _ in range(43))
        s, h, _, _ = raw("GET", "/invite/" + fake, "owner")
        if s != 200:
            skip("/invite/<token> page headers", "page answered %s (expected 200 for a signed-in user)" % s)
        else:
            passfail("/invite/<token>: Referrer-Policy no-referrer and Cache-Control no-store", h.get("referrer-policy") == "no-referrer" and "no-store" in h.get("cache-control", ""), "RP=%s CC=%s" % (h.get("referrer-policy"), h.get("cache-control")))
    finally:
        for d in docs:
            try:
                api("owner", "DELETE", "/documents/" + d)
            except Exception:
                pass
    fails = results.count("FAIL")
    print("\n%d PASS, %d FAIL, %d SKIP" % (results.count("PASS"), fails, results.count("SKIP")))
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())

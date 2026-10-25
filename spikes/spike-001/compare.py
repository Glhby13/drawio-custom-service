#!/usr/bin/env python3
"""DISPOSABLE SPIKE — NOT PRODUCTION CODE: functional diff of result files. Timings, random ids, user agents are excluded.
usage: compare.py A.json B.json [label]   (E8/E9 may live in a separate file: pass extra files with --extra-a/--extra-b)"""
import json, sys, argparse

ap = argparse.ArgumentParser()
ap.add_argument('a'); ap.add_argument('b')
ap.add_argument('--extra-a'); ap.add_argument('--extra-b'); ap.add_argument('--out', default='results/cross-compare.json')
args = ap.parse_args()

def load(p, extra=None):
    e = json.load(open(p))['experiments']
    if extra: e.update(json.load(open(extra))['experiments'])
    return e

def functional(e):
    f = {}
    if 'E0' in e:
        e0 = e['E0']; f['E0'] = {k: e0[k] for k in ('drawioVersion', 'editorPath', 'sameOriginFetch', 'crossOriginFetch', 'websocketSameOrigin', 'httpErrors', 'violations')}
    if 'E1' in e: f['E1'] = {k: (v.get('loaded', v)) for k, v in e['E1'].items()}
    if 'E2' in e: f['E2'] = [{'op': r['op'], 'changeTypes': r['changeTypes'], 'autosaveCount': r['autosaveCount'], 'xmlOnly': r['xmlOnlyAutosaves']} for r in e['E2']]
    if 'E3' in e: f['E3'] = {k: e['E3'][k] for k in ('ops', 'autosaves', 'withoutPatch', 'replayEqual')}
    if 'E4' in e:
        e4 = e['E4']; f['E4'] = {'echoes': e4['echoesOnApply'], 'dup': e4['duplicate'], 'undo': e4['afterLocalUndo'], 'del': e4['remoteDeleteOfSelected'], 'otherPage': e4['remoteWhileOtherPage'], 'before': e4['before'], 'after': e4['after']}
    if 'E5' in e: f['E5'] = [{'id': r['id'], **{s: {k: r[s].get(k) for k in ('convergedAB', 'aEqualsR', 'bEqualsR')} for s in ('S1', 'S3')}} for r in e['E5']]
    if 'E5R' in e: f['E5R'] = [{'id': r['id'], **{m: {k: r[m].get(k) for k in ('convergedAB', 'repaired')} for m in ('merge', 'adapterDiff')}} for r in e['E5R']]
    if 'E6' in e: f['E6'] = [{'cells': r['cells'], 'fullXmlBytes': r['fullXmlBytes'], 'nativePatchBytes': r['nativePatchBytes'], 'checksumsOk': r['checksumsOk']} for r in e['E6']]
    if 'E7' in e:
        e7 = e['E7']; f['E7'] = {'handBuilt': e7['handBuilt'], 'hostile': e7['hostile'], 'polluted': e7['prototypePolluted'], 'orphan': e7['orphanInserted'], 'usable': e7['stillUsable']}
    if 'E8' in e: f['E8'] = e['E8']
    if 'E9' in e: f['E9'] = e['E9']
    return f

a, b = load(args.a, args.extra_a), load(args.b, args.extra_b)
fa, fb = functional(a), functional(b)
common = sorted(set(fa) & set(fb))
diffs = [k for k in common if fa[k] != fb[k]]
out = {'a': args.a, 'b': args.b, 'comparedSections': common, 'onlyInA': sorted(set(fa) - set(fb)), 'onlyInB': sorted(set(fb) - set(fa)), 'functionalIdentical': not diffs, 'differingSections': diffs}
for k in diffs: out.setdefault('details', {})[k] = {'a': fa[k], 'b': fb[k]}
if 'E0' in a and 'E0' in b: out['userAgents'] = {'a': a['E0'].get('ua'), 'b': b['E0'].get('ua')}
print(json.dumps(out, indent=1)[:3500])
json.dump(out, open(args.out, 'w'), indent=1)

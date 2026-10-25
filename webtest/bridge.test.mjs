// diff(T, L): for every step of every Go-generated document history, the ops computed from (state before, state after)
// must turn the predicted state into the live state (ignoring order keys), and toPatch must be empty for identical states.
import { readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const web = join(here, '..', 'internal', 'edge', 'web');
const M = require(join(web, 'model.js'));
const B = require(join(web, 'bridge.js'));
const vectors = JSON.parse(readFileSync(join(here, 'vectors.json'), 'utf8'));

const diffOps = {};
let cases = 0, fail = 0, opsTotal = 0, emptyPatchBad = 0, patchSeen = 0;
const report = (msg) => { fail++; if (fail <= 6) console.log(msg); };
for (const v of vectors) {
  const doc = M.fromState(v.start.state, v.start.seq);
  for (let i = 0; i < v.steps.length; i++) {
    const s = v.steps[i];
    if (s.rejected) continue;
    const before = M.clone(doc);
    s.resolved.forEach((o) => M.apply(doc, o));
    const L = B.docToLive(doc);
    const r = B.diff(before, L);
    cases++; opsTotal += r.ops.length;
    (diffOps[v.seed] = diffOps[v.seed] || {})[i] = r.ops;
    if (r.mismatch) report(`MISMATCH seed ${v.seed} step ${i} (${s.authored.map((o) => o.t).join(',')}) ops=${r.ops.map((o) => o.t).join(',')}`);
    // every produced op must be accepted by the (Go-mirrored) apply and be well-formed
    for (const o of r.ops) if (!o.id || !o.t) report('op without id/t');
    // identical states give an empty patch
    const same = B.toPatch(B.docToLive(doc), doc);
    if (!same.empty) emptyPatchBad++;
    const p = B.toPatch(B.docToLive(before), doc);
    if (!p.empty) patchSeen++;
  }
}
writeFileSync(join(here, 'diffops.json'), JSON.stringify(diffOps));
console.log(`${cases} diff cases, ${opsTotal} ops, ${fail} mismatches, ${emptyPatchBad} non-empty self-patches, ${patchSeen} non-empty patches`);
process.exit(fail || emptyPatchBad ? 1 : 0);

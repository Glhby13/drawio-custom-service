// Conformance: the browser model must reproduce the Go model byte for byte (Canon hash after every step), both when it
// applies the server's RESOLVED ops (confirmed path) and when it resolves AUTHORED ops itself (optimistic path).
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const M = require(join(here, '..', 'internal', 'edge', 'web', 'model.js'));
const vectors = JSON.parse(readFileSync(join(here, 'vectors.json'), 'utf8'));

let steps = 0, fail = 0;
for (const v of vectors) {
  const confirmed = M.fromState(v.start.state, v.start.seq);
  const optimistic = M.fromState(v.start.state, v.start.seq);
  for (let i = 0; i < v.steps.length; i++) {
    const s = v.steps[i];
    if (s.rejected) continue; // the server refused the batch; the client never applies refused batches
    s.resolved.forEach((o) => M.apply(confirmed, o));
    s.authored.forEach((o) => M.applyAuthored(optimistic, o));
    const hc = await M.hash(confirmed), ho = await M.hash(optimistic);
    steps++;
    if (hc !== s.hash || ho !== s.hash) {
      fail++;
      if (fail <= 3) {
        console.log(`MISMATCH seed ${v.seed} step ${i} confirmed=${hc === s.hash} optimistic=${ho === s.hash} ops=${s.authored.map((o) => o.t).join(',')}`);
        const a = M.canon(confirmed).split('\n'), b = M.canon(optimistic).split('\n');
        for (let k = 0; k < Math.max(a.length, b.length); k++) if (a[k] !== b[k]) { console.log('  confirmed :', a[k]); console.log('  optimistic:', b[k]); break; }
      }
    }
  }
  if (M.canon(confirmed) !== v.canon) { fail++; console.log(`FINAL CANON MISMATCH seed ${v.seed}`); }
}
console.log(`${steps} steps over ${vectors.length} documents, ${fail} mismatches`);
// fractional index parity with Go
let bad = 0;
const keys = [''];
for (let i = 0; i < 5000; i++) {
  keys.sort();
  const j = (i * 7919) % (keys.length + 1);
  const a = j > 0 ? keys[j - 1] : '', b = j < keys.length ? keys[j] : '';
  if (b !== '' && a >= b) continue;
  const k = M.between(a, b);
  if (!M.validKey(k) || (a !== '' && k <= a) || (b !== '' && k >= b)) bad++;
  keys.push(k);
}
console.log(`fractional index: ${bad} invalid`);
process.exit(fail || bad ? 1 : 0);

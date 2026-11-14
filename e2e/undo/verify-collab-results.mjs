// Author / Agent: Codex, 2026-11-15
import fs from 'node:fs';
import assert from 'node:assert/strict';
import { checkCollab } from './collab-checks.mjs';
const latest = new Map();
for (const file of process.argv.slice(2)) {
  const data = JSON.parse(fs.readFileSync(file));
  assert.equal(data.errors.length, 0, file + ': incomplete run');
  for (const key of ['s1', 's2', 's3', 's4', 's5']) for (const r of data[key]) {
    const tag = key === 's1' ? `S1 3user-${r.own ? 'disjoint' : 'shared'}`
      : key === 's2' ? 'S2 container' : key === 's3' ? `S3 conflict ${r.mode}`
      : key === 's4' ? 'S4 pages' : `S5 ${r.kind}`;
    latest.set(`${tag} d=${r.delay}`, r);
  }
}
assert.equal(latest.size, 16, 'expected complete two-delay collaboration matrix');
for (const [tag, r] of latest) {
  assert.ok(checkCollab(tag, r), tag + ': semantic or protocol failure');
  console.log(`PASS ${tag} nacks=${r.serverNacks}`);
}
console.log('PASS: all 16 collaboration scenarios, including semantic and post-conflict recovery checks');

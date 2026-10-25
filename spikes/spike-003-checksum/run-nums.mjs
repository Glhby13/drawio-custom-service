// DISPOSABLE SPIKE — NOT PRODUCTION CODE: JS num() vs Go num() on 200k doubles.
import { createRequire } from 'node:module';
import { spawnSync } from 'node:child_process';
const require = createRequire(import.meta.url);
const C = require('./canon.js');
const bin = process.env.CANON_BIN || '/tmp/spike003-canon';
const edge = [0, -0, 1, -1, 0.5, 0.03125, 0.1, 0.2, 0.1 + 0.2, 1e21, 1e-7, 1e-6, 123456789012345680000, 5e-324, 2.2250738585072014e-308, 1.7976931348623157e308,
  100, 100.0, 1.005, 2.675, 4.35, 0.015625, 8.5, 1e15, 1e16, 9007199254740993, 0.000001, 0.0000001, -1e-7, 33.333333333333336];
let seed = 12345; const rnd = () => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed / 4294967296; };
const nums = [...edge];
for (let i = 0; i < 60000; i++) nums.push(Math.round((rnd() - 0.5) * 2e5) / 100);                 // typical geometry, 2 decimals
for (let i = 0; i < 60000; i++) nums.push((rnd() - 0.5) * Math.pow(10, Math.floor(rnd() * 40 - 20))); // wide magnitude
const f64 = new Float64Array(1), u32 = new Uint32Array(f64.buffer);
for (let i = 0; i < 80000; i++) { u32[0] = (rnd() * 4294967296) >>> 0; u32[1] = (rnd() * 4294967296) >>> 0; if (isFinite(f64[0])) nums.push(f64[0]); }
const js = nums.map(C.num);
const r = spawnSync(bin, ['nums'], { input: JSON.stringify(nums), maxBuffer: 1 << 30 });
if (r.status !== 0) { console.error(r.stderr.toString()); process.exit(1); }
const go = JSON.parse(r.stdout.toString());
let diff = 0; const samples = [];
for (let i = 0; i < nums.length; i++) if (js[i] !== go[i]) { diff++; if (samples.length < 8) samples.push({ v: nums[i], js: js[i], go: go[i] }); }
console.log(JSON.stringify({ node: process.version, checked: nums.length, mismatches: diff, samples }, null, 1));
process.exit(diff ? 1 : 0);

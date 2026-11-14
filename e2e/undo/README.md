# DrawSpace undo browser regression

Author / Agent: Codex
Timestamp: 2026-11-15 Asia/Jakarta

These tests use real headless Chrome, unmodified official draw.io v32.6.1 web assets, the production adapter/bridge/model, and a Go validator compiled from this checkout. The local WebSocket coordinator is a test implementation; it does not exercise production authentication, PostgreSQL, Redis, S3, access control, or deployment.

Prerequisites: Go matching go.mod, the existing e2e Playwright dependency, Chrome, and a local official draw.io checkout. No credentials are needed. Tests bind only to loopback. Do not point DRAWIO_WEBAPP at custom editor code.

```bash
# From the repository root; set this to your existing official source checkout.
export DRAWIO_WEBAPP=/absolute/path/to/drawio/src/main/webapp
go build -o e2e/undo/.validator ./e2e/undo/validator
node e2e/undo/repro-abc-undo.mjs
node e2e/undo/repro-patch-recovery.mjs
node e2e/undo/repro-rootonly-page.mjs
node e2e/undo/massive-solo.mjs
node e2e/undo/massive-collab.mjs
```

`DRAWIO_VALIDATOR` overrides the validator binary path. Chrome currently uses the standard macOS application path. `DELAYS`, `ONLY`, and (collab only) `REPEAT` allow focused reproduction; inspect each runner for scenario names. Full solo matrix: seven scenarios × three delays (0/150/400 ms) × keyboard/toolbar = 42 runs. Full collaboration matrix: eight scenarios × two delays (0/250 ms) = 16 runs. These are bounded regression matrices, not exhaustive coverage of all possible interleavings.

Solo checks snapshots at undo/redo boundaries, semantic state, hash convergence, NACKs, exceptions, and diff mismatch. Collaboration additionally checks restored children/parents, preservation of another user's edits, page count, offline edit survival, and reconnect. Concurrent random deletion/revival may reject an atomic batch with `cell_gone`, `terminal_gone`, or `duplicate_cell`; only S1 allows those codes, verifies the failed target is absent (or already live for duplicate revival) in the authoritative state, and requires fresh edits from all three users to save afterward. It reports `PASS_WITH_LIFECYCLE_CONFLICT` and the rejection count, never silently reports zero errors. Other scenarios require zero NACKs. A rejected atomic batch is not saved; the application displays a warning.

Toolbar fallback to the editor action API is counted in stress reports. Consequently those runs cover both DOM clicks and direct action dispatch; they do not claim every toolbar attempt was a physical click. Programmatic graph changes prepare fixtures, while undo uses keyboard/toolbar actions. Full resync deliberately clears old undo history; catchup reconnect preserves it. Results JSON and validator binaries are ignored by Git. The scripts exit nonzero on failed assertions or incomplete runs. For a focused S1 rerun, use `ONLY=s1 OUT=e2e/undo/collab-stress.json node e2e/undo/massive-collab.mjs`, then validate the combined saved evidence with `node e2e/undo/verify-collab-results.mjs e2e/undo/massive-collab.json e2e/undo/collab-stress.json`. Later records replace the same scenario; missing scenarios fail.

Current evidence and limitations: [VERIFICATION.md](VERIFICATION.md). Live integration suites remain in the parent e2e directory and require a healthy lab.

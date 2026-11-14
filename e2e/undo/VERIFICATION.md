# Undo regression verification — 2026-11-15

Author / Agent: Codex
Timestamp: 2026-11-15 Asia/Jakarta

## Scope and before/after

Production fix under verification: `202d30e`, already on origin/main at session start. This follow-up promotes temporary browser scripts into versioned, reproducible tests, compiles their validator against the actual production Go package, makes failures exit nonzero, adds explicit A/B/C assertions and fault recovery checks, and strengthens semantic checks for collaboration.

Before: applying the new model regression tests to `202d30e^:internal/model/ops.go` with a temporary Go overlay fails: deleted-cell revival returns `duplicate_cell`, deleted-page revival returns `duplicate_page`, and restore guard test panics on a nil cell. After: all model tests pass under the race detector. The overlay was ignored temporary evidence, not a source edit or change to the baseline commit.

| Cause | Before | After (`202d30e`) |
|---|---|---|
| Tombstones | Undo reconstructs an ID that validation treats as a duplicate; restore can dereference nil | Revival accepts deleted IDs, preserves LWW guards, skips nil/alive/stale restores |
| Full reload | Root-change entries remain in editor history; undo can detach the current root/autosave path | Clears root undo history after full reload; new edits/undo save normally |
| Partial remote page | Patch arrives before root/layer; editor invents root/layer and sends duplicate additions | Bounded readiness wait, with release for valid root-only pages |
| Failed patch | Missing/error patch response leaves reconciliation uncertain | Error or 4s timeout requests full resync and resumes editing |

## Evidence collected in this session

- `go test -race -count=1 ./...`: pass (all production packages).
- `golangci-lint run --timeout 10m ./cmd/... ./internal/...`: 0 issues.
- `go vet ./...` and `CGO_ENABLED=0 go build ./cmd/...`: pass.
- JS model parity: 1,799 steps, 40 documents, zero mismatches. Bridge: 1,799 diff cases, 1,946 operations, zero mismatches or nonempty self-patches.
- Fault injection: patch error resync ~661ms; missing patch response resync ~4634ms. Both clients converged, zero NACKs; post-recovery edit/undo converged.
- Root-only page: held initially, released ~298–365ms, two pages, matching confirmed/live hashes, zero NACKs or browser errors.
- A/B/C assertion rerun: pass, including redo and empty-history boundary; zero NACKs.
- Collaboration: all 12 targeted records pass the strengthened semantic checker; all four S1 stress reruns pass lifecycle-state/recovery checks. Final stress records contain 20 rejected atomic batches (19 `cell_gone`, one `terminal_gone`), explicitly reported rather than counted as zero. All three users could save independent recovery markers. No browser exceptions, diff mismatches, hash errors or runtime errors. Delays 0/250ms, shared and disjoint edits.
- Initial stress oracle required zero NACKs and therefore exited nonzero on valid concurrent deletion/revival conflicts. Investigation checked failed operations against authoritative state; the corrected oracle permits only explained lifecycle conflicts in S1 and also requires fresh saves by all users. Tombstone revival regressions and all 12 targeted scenarios continue to require zero NACKs. The rerun did not change production conflict handling.
- Combined evidence command: `node e2e/undo/verify-collab-results.mjs e2e/undo/massive-collab.json e2e/undo/collab-stress.json` — pass, complete 16-scenario matrix. Older S1 records are replaced by fresh S1 evidence; the 12 original targeted records are re-evaluated with current assertions.
- Solo browser matrix: **42/42 pass**, elapsed 1609s, seven scenario families × three delays (0/150/400ms) × keyboard/toolbar. Nested swimlanes/tables, group/ungroup/clone/edges/delete, 24-edit bursts, inline labels, format inputs/style/text/arrange, multi-page undo, and sidebar Misc/UML/ER/Flowchart shapes. 1,170 expected state transitions; zero unexpected NOOP/MULTI/LAG/UNKNOWN, NACKs, resyncs, hash errors, exceptions, diff mismatches, bad probes/checkpoints or unconverged states. Native input-history entries (6) and page-selection steps (30) were explicitly expected.
- Automation limitation: 56 toolbar click timeouts were recovered by observing the already-fired action (49) or editor action fallback (7). Thus this is not a claim that all toolbar checks completed through an uninterrupted DOM click. State oracles still passed. Browser console warning/error records: zero.

## A/B/C scenario

Three independent editor sessions each insert one shape in order A → B → C. B Undo removes B only; A and C remain on all three clients. B Redo restores B. Another Undo at the empty-history boundary does not remove A or C. The history belongs to the local editor session, not a global timeline and not permanent object ownership.

If B edited a shape shared with C, undo may affect that shared shape. A collaborator deleting the target can cause `cell_gone`/`terminal_gone`; simultaneous revival of an already-live ID can cause `duplicate_cell`. The coordinator rejects the entire batch atomically and warns the sender. That is distinct from rejecting a tombstoned ID (the fixed bug). Random stress must record these conflicts, verify the target's authoritative lifecycle state explains them, converge, and save independent new edits afterward. It does not promise every conflicting edit is accepted.

## Limits

Official draw.io v32.6.1 runs in real headless Chrome. Fixtures use graph APIs; undo/redo use keyboard and toolbar (counted action fallback where DOM toolbar controls are unavailable). The local coordinator uses the current production Go model and checks JS/Go hashes, but substitutes production WebSocket transport, authentication, and storage.

Live `drawio.lab.local`, Jenkins and ArgoCD checks remain blocked: `ssh lab-bastion` reports `No route to host` to 192.168.64.2. No live deployment success is claimed. Existing live permission/auth/persistence suites must run after the lab is reachable. Office unchanged/pending; no DDL/env/Secret/Helm changes. No finite regression suite guarantees every possible browser/network/concurrency interleaving.

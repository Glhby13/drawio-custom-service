# Architecture Decision Records — drawio-custom-service

Author / Agent: Claude Code
Timestamp: 2026-10-25 00:05

Format ringkas MADR: **Status · Context · Decision · Consequences · Alternatives**. Status yang dipakai:
`Proposed` (hipotesis, menunggu bukti) · `Accepted` · `Accepted (provisional)` (boleh diubah dengan kriteria yang ditulis di ADR) ·
`Superseded by ADR-xxxx` · `Rejected`.

Label bukti di semua ADR: **EVIDENCE** (source code, dokumentasi resmi, eksperimen yang benar-benar dijalankan dengan hasil tercatat),
**INFERENCE** (penalaran/pengetahuan umum), **UNKNOWN** (belum dibuktikan).

| ADR | Judul | Status |
|---|---|---|
| [0001](0001-non-invasive-extension-of-drawio.md) | Ekstensi draw.io tanpa mengubah draw.io | Accepted |
| [0002](0002-stateless-pods-external-state.md) | Pod stateless, state di sistem eksternal | Accepted |
| [0003](0003-deployment-topology-gitops.md) | Topologi deploy GitOps | Accepted |
| [0004](0004-edge-authn-vs-domain-authz.md) | Autentikasi di edge, otorisasi di domain | Accepted |
| [0005](0005-initial-service-decomposition.md) | Dekomposisi service awal | Accepted (provisional; 2 deployable → 3 di Phase 6) |
| [0006](0006-collaboration-change-model.md) | Model perubahan & kolaborasi | **Proposed** |
| [0007](0007-document-persistence-snapshot-oplog.md) | Persistensi: snapshot + operation log | Accepted (struktur PG op log); angka retensi menunggu VM lab/office |
| [0008](0008-mcp-server.md) | MCP server | Proposed |
| [0009](0009-ai-as-authorized-collaborator.md) | AI sebagai kolaborator berotorisasi | Proposed |
| [0010](0010-semantic-operation-contract.md) | Semantic Operation Contract | **Proposed** (direvisi oleh bukti SPIKE-002) |
| [0011](0011-cross-platform-architecture.md) | Arsitektur lintas-platform (lab ARM64 ↔ office AMD64) | Accepted (invarian) · Proposed (variabel platform) |
| [0012](0012-build-and-runtime-compatibility.md) | Kompatibilitas build dan runtime | Accepted (build) · Proposed (runtime RHEL/RKE) |

Template ADR baru:
```markdown
# ADR-NNNN — Judul
Author / Agent: …
Timestamp: YYYY-MM-DD HH:MM
Status: Proposed
## Context
## Decision
## Consequences
## Alternatives
## Evidence
```

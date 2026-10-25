# ADR-0005 — Dekomposisi service awal

Author / Agent: Claude Code
Timestamp: 2026-10-25 00:10 (revisi 2026-10-25 02:35)
Status: Accepted (provisional) — direvisi mengikuti bukti SPIKE-002

## Context
Kandidat awal: `gateway`, `document-service`, `collab-service`, `mcp-server`. Tujuannya bukan "microservices", melainkan batas
yang benar: keamanan, skala, dan siklus rilis. Collab dan documents berbagi model domain, PDP izin, dan op log; memisahkannya
terlalu dini membuat setiap op melewati jaringan (chatty) dan menggandakan logika.

## Decision
**Bounded context** (paket `internal/` dengan antarmuka eksplisit, tidak saling mengimpor internal paket lain):
`edge` (sesi/OIDC/proxy), `identity` (token internal), `documents` (metadata, snapshot, versi), `authz` (PDP),
`oplog` (sequencer, append, kompaksi), `collab` (sesi realtime, presence, fan-out), `mcp` (tools/resources), `audit`.

**Deployable awal (3)**:
| Deployable | Isi | Alasan |
|---|---|---|
| `gateway` | edge + identity + proxy `/editor/*` + host page/adapter statis | batas keamanan internet-facing; skala & rilis beda |
| `document-collab` | documents + authz + oplog + collab + audit | satu konsistensi transaksional untuk op (authorize → validate → sequence → persist → fan-out) tanpa hop jaringan |
| `mcp-server` | mcp (memanggil API domain lewat HTTP internal) | batas keamanan berbeda (token agen), skala & rilis independen, isolasi kegagalan dari UI |

Pemisahan `document-collab` menjadi `document-service` + `collab-service` dilakukan **hanya** bila kriteria pisah terpenuhi.

### Kriteria pisah
- Skala independen (mis. koneksi WebSocket ≫ request REST, butuh replika berbeda).
- Siklus rilis independen yang sering terhalang satu sama lain.
- Batas keamanan berbeda (data/credential berbeda).
- Kepemilikan tim berbeda.
- Isolasi kegagalan (crash collab menjatuhkan REST, terbukti di insiden/uji).

### Kriteria gabung
- Panggilan chatty (> 1 RPC sinkron per op/request di jalur panas).
- Tidak ada kebutuhan skala independen.
- Logika duplikat (validasi/izin) di dua deployable.
- Overhead operasional (chart, alert, Secret) lebih besar dari manfaat.
- Batas jaringan yang tidak memberi keamanan tambahan.

## Revisi berdasarkan bukti (SPIKE-002)
Keputusan diubah dari "3 deployable sejak awal" menjadi **2 deployable sampai Phase 5, 3 mulai Phase 6**:

| Fase | Deployable | Alasan |
|---|---|---|
| 2–5 | `gateway` + `platform` (documents + authz + oplog + collab + audit) | cukup untuk SSO, simpan, share, kolaborasi; MCP belum ada |
| 6+ | + `mcp-server` | batas keamanan berbeda (token agen), rilis independen; baru ada kebutuhannya di Phase 6 |

Bukti yang dipakai (EVIDENCE kecuali ditandai):
- CPU sequencing + apply + encode ≤ 29 µs/op p99 (2–5 µs pada run tenang); 10.000 op/s ≤ 7% satu core → **tidak ada alasan skala CPU** untuk memisahkan collab dari documents.
- Biaya per op didominasi commit PostgreSQL (±1–5 ms; 1,1 ms per transaksi tanpa batching) dan draw.io di browser (28–269 ms); sebuah hop RPC internal (INFERENCE ±0,2–1 ms) di jalur
  `authorize → validate → sequence → commit → broadcast` menambah kopling dan satu mode kegagalan baru tanpa manfaat terukur.
- Jalur panas butuh transaksi yang sama (alokasi sequence + insert op + cek izin): satu proses menjaga satu transaksi (group commit 2 ms → 9.946 op/s, p99 8 ms pada satu dokumen).
- Gateway tetap terpisah: ia satu-satunya komponen yang menghadap pengguna (OIDC, CSRF, proxy `/editor/`, WebSocket upgrade) dan punya profil serangan/rilis berbeda.
- Kriteria pisah `platform` → `documents` + `collab` yang masih berlaku: skala koneksi WebSocket ≫ REST (diukur di Phase 5/8), insiden isolasi kegagalan, atau kepemilikan tim terpisah.

## Consequences
- Satu image multi-target (`SERVICE_NAME`) seperti n8n-custom-service; menambah/memecah deployable = target baru + element ApplicationSet.
- `platform` (dulu `document-collab`) multi-replika butuh *single sequencer per dokumen*: kepemilikan dokumen per replika (lease di PostgreSQL/Redis) atau
  sequence via PostgreSQL (`SELECT … FOR UPDATE` / sequence per dokumen). Keputusan di ADR-0007 setelah benchmark DB.
- MCP tidak punya jalur ke DB/S3; hanya API domain (ADR-0008).

## Alternatives
- Empat service sejak awal — ditunda (kriteria pisah belum terbukti; menambah RPC di jalur op).
- Satu binary untuk semuanya — ditolak: gateway dan MCP adalah batas keamanan yang berbeda.

## Evidence
INFERENCE. Benchmark CPU op log (SPIKE-001 §23, `spikes/spike-002-oplog`) menunjukkan sequencing+apply bukan bottleneck
(p99 ≤ 29 µs/op), jadi pemisahan demi performa CPU tidak dibenarkan saat ini.

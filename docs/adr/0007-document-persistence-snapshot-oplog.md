# ADR-0007 — Persistensi: snapshot + operation log

Author / Agent: Claude Code
Timestamp: 2026-10-25 01:10 (revisi 2026-10-25 02:40)
Status: Proposed → **Accepted untuk struktur op log PostgreSQL** (benchmark disposable lulus); angka retensi/partisi dan benchmark di VM PostgreSQL lab/office masih menunggu

## Context
Tanpa PVC (ADR-0002). Butuh: buka dokumen cepat, riwayat versi, audit, recovery setelah restart, dan basis untuk kolaborasi (ADR-0006).
Biaya draw.io memuat XML penuh: ±1 MB / 37 KB terkompresi untuk 5.000 cell (EVIDENCE E6). Op ±270 B JSON (EVIDENCE spike-002).

## Decision
```
S3 (SeaweedFS) bucket drawio-documents
 └── documents/<documentId>/snapshots/<serverSeq>.drawio.gz   (XML mxfile standar, gzip)
 └── documents/<documentId>/exports/<versionId>.<png|svg>      (opsional)

PostgreSQL drawio_db
 ├── documents          (id, workspace, folder, title, owner, created/updated, current_snapshot_seq, head_seq, deleted_at)
 ├── permissions        (document/folder, subject user|group, role, granted_by, …)
 ├── operations         (document_id, server_seq, operation_id UNIQUE, schema_version, actor_id, actor_type, on_behalf_of,
 │                       agent, model, type, payload jsonb, previous_values jsonb, status, created_at)  PK (document_id, server_seq)
 ├── snapshots          (document_id, server_seq, s3_key, sha256, size, created_at)
 ├── versions           (document_id, version_id, name, server_seq, created_by, created_at)   — versi bernama = pointer ke seq
 └── audit_events       (keputusan authz deny, share, delete, restore, export, login AI, …)

Redis
 └── presence, sesi, pub/sub fan-out, cache keputusan authz — bukan sumber kebenaran
```
- **Ack setelah commit**: op dianggap diterima setelah baris `operations` ter-commit; baru kemudian di-broadcast.
- **Snapshot**: ditulis setiap K op atau T detik idle (awal: K=500, T=60 s, INFERENCE), dan saat versi bernama dibuat. Snapshot =
  XML draw.io standar (tanpa lock-in, bisa dibuka draw.io mana pun).
- **Recovery**: state = snapshot terbaru (`snapshots`) + `operations` dengan `server_seq` > snapshot. Bila objek S3 hilang/rusak
  (sha256 tidak cocok) → snapshot sebelumnya + replay lebih panjang.
- **Kompaksi**: op lebih tua dari snapshot dan lebih tua dari jendela retensi (awal 90 hari, INFERENCE) dipindah ke arsip/dihapus;
  ringkasan audit tetap. Versi bernama tidak dikompaksi.
- **Sequencer**: satu penulis per dokumen. Kandidat: `UPDATE documents SET head_seq = head_seq + 1 … RETURNING` dalam transaksi insert op
  (sederhana, aman multi-replika) vs lease kepemilikan dokumen per replika. Dipilih setelah benchmark (SPIKE-001 §9).
- **Format persistensi internal**: kolom `payload` mengikuti Semantic Operation `schemaVersion` saat ditulis; tidak diekspos sebagai API.

## Bukti benchmark (SPIKE-002 §PostgreSQL)
PostgreSQL 18.6 (Docker Desktop arm64, `synchronous_commit=on`, `wal_sync_method=fdatasync`, `shared_buffers=128MB`, tabel `ops` PK `(document_id, server_seq)` + unique `(document_id, operation_id)`, payload jsonb ±270 B), Go + `pgx` v5, pool 32, open-loop, warm-up + `CHECKPOINT` per run, Mac berbagi CPU dengan 8 VM lab (kondisi ukur, bukan lingkungan server):

| Target | Dokumen | Batching | Tercapai | p50 | p95 | p99 | Catatan |
|---|---|---|---|---|---|---|---|
| 100 op/s | 1 | tidak | 100/s | 2,7 ms | 4,5 ms | 7,0 ms | |
| 1.000 op/s | 1 | tidak | **934/s (jenuh)** | 312 ms | 662 ms | 709 ms | ±1,07 ms/transaksi, serial pada baris dokumen |
| 1.000 op/s | 1 | 2 ms | 1.000/s | 4,1 ms | 6,9 ms | 8,5 ms | rata-rata batch 4,1 |
| 10.000 op/s | 1 | 2 ms | 9.946/s | 3,9 ms | 6,5 ms | 8,1 ms | rata-rata batch 39,6 |
| 1.000 op/s | 200 | tidak | 1.000/s | 1,1 ms | 2,2 ms | 3,4 ms | commit paralel (group commit WAL) |
| 10.000 op/s | 200 | tidak | **6.724/s (jenuh)** | 1.018 ms | 1.716 ms | 1.783 ms | pool 32 koneksi |
| 10.000 op/s | 200 | 2 ms | 9.955/s | 39,6 ms | 56,1 ms | 66,5 ms | batch kecil (1,4) + antrean pool |

- Beban realistis per dokumen (5–50 op/s, EVIDENCE E6: 1 gestur ≈ 1 op) jauh di bawah titik jenuh 934 op/s/dokumen; **batching per dokumen hanya perlu** untuk dokumen dengan > ±500 op/s (mis. AI batch besar).
- Ukuran: **440 B/op termasuk indeks** (1 juta op = 441 MB) → 1.000 op/s terus-menerus ≈ 38 GB/hari tanpa kompaksi.
- Replay: 1.000 op 5 ms; 100.000 op 167 ms; 1 juta op 1,6 s (fetch + scan). Celah reconnect 10 op p95 0,5 ms; 1.000 op p95 2,5 ms; 100.000 op p95 116 ms.
- Snapshot 40 KB `bytea` insert p50 1,0 ms.
- Kompaksi: `DELETE` 500.000 baris 266 ms + `VACUUM` 405 ms, **ukuran tabel tidak turun** (441 MB → 441 MB; ruang dipakai ulang, tidak dikembalikan). **Keputusan:** `ops` dipartisi (`PARTITION BY HASH(document_id)` untuk sebaran, dan/atau per rentang `created_at` bulanan untuk retensi) agar retensi = `DROP PARTITION`, bukan `DELETE`.
- Ketidakpastian: disk Docker Desktop (virtio, semantik fsync tidak sama dengan SSD server); angka absolut **harus diulang di VM PostgreSQL lab dan server office** dengan program yang sama (`spikes/spike-005-pg`).

## Consequences
- Buka dokumen = 1 GET S3 + query op sejak snapshot (biasanya < K op).
- Pertumbuhan DB tanpa kompaksi ≈ 38 GB/hari pada 1.000 op/s terus-menerus (EVIDENCE: 440 B/op) → retensi lewat partisi wajib; beban nyata di lab jauh lebih kecil (5–50 op/s per dokumen aktif).
- Backup: PostgreSQL (dump/PITR) + replikasi/backup bucket S3 harus konsisten (snapshot menunjuk seq yang ada di DB) — prosedur DR di MASTER-PLAN §19.

## Alternatives
- Hanya snapshot (tanpa op log) — ditolak: tanpa audit per op, tanpa replay/revert granular, kolaborasi tidak punya urutan.
- Hanya op log (tanpa snapshot) — ditolak: replay panjang tak terbatas.
- Git sebagai penyimpanan utama — ditolak untuk realtime (latensi, konflik); tetap kandidat **ekspor/arsip** versi bernama.

## Evidence
E6 (ukuran XML), `spikes/spike-002-oplog/results.json` (biaya CPU replay/encode). Latensi DB: UNKNOWN (metodologi SPIKE-001 §9).

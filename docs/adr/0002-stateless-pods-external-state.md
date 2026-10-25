# ADR-0002 — Pod stateless, state di sistem eksternal

Author / Agent: Claude Code
Timestamp: 2026-10-25 00:05
Status: Accepted

## Context
Cluster lab tidak punya StorageClass; user mensyaratkan **tanpa PVC** untuk draw.io maupun service custom. Kantor nantinya
air-gapped on-prem. Sistem eksternal yang sudah ada di lab: PostgreSQL 18 (`192.168.128.10:5432`), SeaweedFS S3
(`http://192.168.128.9:8333`), Redis 8 (`192.168.128.11:6379`), git (GitHub lab / GitLab kantor).

## Decision
| Data | Tempat | Sumber kebenaran? |
|---|---|---|
| Snapshot dokumen (XML `.drawio`, terkompresi) | SeaweedFS S3, bucket khusus | ya (bersama op log) |
| Metadata dokumen, izin/share, operation log, audit, versi bernama | PostgreSQL DB khusus (`drawio_db`, owner app user) | ya |
| Sesi web, presence, fan-out pub/sub, rate-limit counter | Redis (prefix/DB terpisah) | **tidak** — boleh hilang |
| Konfigurasi | Helm values (git) + Secret K8s buatan tangan | ya |
| Ekspor ke git (opsional, fitur masa depan) | git | tidak (salinan) |

- Semua pod: tanpa `PersistentVolumeClaim`; `emptyDir` hanya untuk file sementara.
- Kehilangan pod/Redis tidak boleh menghilangkan data yang sudah di-ack ke klien. Op di-ack **setelah** commit PostgreSQL (ADR-0007).
- Image draw.io stateless (EVIDENCE: Dockerfile tanpa `VOLUME`; diagram tidak pernah disimpan di server draw.io).

## Consequences
- Collab-service harus bisa memulihkan state dokumen dari snapshot S3 + op log setelah restart (diuji di Phase 5/8).
- Redis down → presence/kolaborasi realtime terganggu, tetapi simpan via REST tetap jalan (degradasi ke snapshot+lock).
- Latensi tulis op = latensi commit PostgreSQL; target diukur di Phase 5 (metodologi SPIKE-001 §23).

## Alternatives
- PVC/Longhorn — ditolak (tidak ada StorageClass; RAM node lab, lihat memori `project_longhorn_feasibility`).
- Menyimpan op log di Redis Streams sebagai sumber kebenaran — ditolak: durabilitas dan backup lebih lemah dari PostgreSQL.
- Menyimpan semua di S3 (tanpa DB) — ditolak: query izin/riwayat dan urutan op butuh transaksi.

## Evidence
SPIKE-000 §10; INFERENCE untuk perilaku failover (diuji di Phase 8).

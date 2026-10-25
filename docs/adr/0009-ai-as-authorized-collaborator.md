# ADR-0009 — AI sebagai kolaborator berotorisasi

Author / Agent: Claude Code
Timestamp: 2026-10-25 01:15
Status: Proposed (Phase 7, di atas MCP)

## Context
AI harus bisa membaca dan mengubah diagram seperti kolaborator biasa: terlihat, dibatasi izin, diaudit, dan dapat dibatalkan.
Tidak boleh ada jalur khusus yang melewati otorisasi.

## Decision
### Identitas
- `actorType = ai`. Dua mode:
  1. **Delegated** (default): user menjalankan agen; token MCP diperoleh lewat OAuth atas nama user, lalu edge menerbitkan token internal
     `sub = agent-id`, `onBehalfOf = user-id`. Bila memakai Keycloak token exchange, klien agen terdaftar sebagai client terpisah.
  2. **Service account**: agen otomatis (mis. pembaru diagram arsitektur terjadwal) dengan service account Keycloak sendiri dan izin eksplisit
     per folder/dokumen.
- **Izin efektif = izin AI ∩ izin user pendelegasi** (delegated) atau izin service account (service). Dihitung PDP domain.

### Kebijakan
- Kebijakan workspace/dokumen: AI dilarang/diizinkan; jenis op yang boleh (mis. tanpa `deleteCell`/`deletePage`); batas op per menit dan per batch;
  mode **saran** opsional (op AI masuk cabang usulan, diterapkan saat user menerima) — desain cabang = UNKNOWN, dievaluasi di Phase 7.
- Konfirmasi manusia untuk aksi destruktif massal (mis. menghapus > N cell) — ditegakkan server, bukan di prompt agen.

### Kehadiran dan atribusi
- AI muncul di presence (nama agen + "atas nama <user>"), seleksi/kursor AI ditampilkan sebagai kolaborator.
- Setiap op AI tercatat dengan `actorType`, `actorId`, `onBehalfOf`, `agent`, `model`, `operationId`, `batchId` (ADR-0010, ADR-0007).

### Undo / revert / audit
- Op AI masuk op log yang sama; revert per batch AI = op invers dari `previous_values` (ADR-0010), dijalankan sebagai op baru oleh user yang meminta.
- Audit dapat difilter per agen/model/user pendelegasi.

### Tanpa jalur belakang
```
AI ─> MCP/API ─> domain authorization ─> semantic operation ─> sequencer/oplog ─> broadcast
```
Bukan `AI ─> S3` dan bukan `AI ─> PostgreSQL`. mcp-server tidak memiliki kredensial penyimpanan (ADR-0008).

## Consequences
- Konten diagram yang dikirim ke model AI eksternal = keputusan kebijakan data; di lingkungan air-gapped, model harus on-prem (UNKNOWN: model yang tersedia di kantor).
- Rate limit dan batas batch melindungi editor manusia dari banjir op (performa draw.io O(N) per perubahan, SPIKE-001 §8).

## Alternatives
- AI menulis file/XML langsung — ditolak (tanpa izin per op, tanpa audit granular).
- AI dengan izin admin global — ditolak (melanggar least privilege).

## Evidence
INFERENCE (desain); bergantung pada ADR-0004, ADR-0008, ADR-0010.

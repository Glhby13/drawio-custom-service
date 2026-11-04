# ADR-0018 — Hapus diagram = Trash, dengan Restore (soft delete yang bisa dibalik)

Author / Agent: Claude Code
Timestamp: 2026-11-04 18:20
Status: **Accepted (2026-11-04, permintaan user saat uji di kantor)** — melengkapi ADR-0004 (PDP) dan ADR-0016 (aktor AI tidak menghapus).

## Context
Backend sudah punya `DELETE /api/v1/documents/{id}` (soft delete: `documents.deleted_at`), tetapi UI tidak punya tombol dan tidak ada jalan kembali.
Pengguna yang salah hapus tidak punya cara memulihkan tanpa akses database.

## Decision
- **Hapus = pindah ke Trash.** `DELETE /api/v1/documents/{id}` tetap soft delete (tanpa DDL baru: kolom `deleted_at` sudah ada). Hak: aksi `delete` di PDP, yaitu **pemilik** atau
  **admin aplikasi**; editor, viewer, dan aktor AI (termasuk token MCP) tidak bisa.
- **Trash**: `GET /api/v1/trash` memuat diagram terhapus yang boleh dipulihkan pemanggil (milik sendiri; admin melihat semua), terbaru dulu, maksimal 500.
  Aktor AI ditolak (403).
- **Restore**: `POST /api/v1/documents/{id}/undelete` (aturan yang sama dengan menghapus). Pihak yang tidak berhak, atau dokumen yang tidak ada di Trash, mendapat **404**
  (Trash bukan oracle keberadaan). Share dan seluruh riwayat/versi ikut kembali karena tidak pernah dihapus.
- **Audit**: `delete` (sudah ada) dan `undelete` (`ok`/`denied`) dengan judul.
- **UI**: ikon tempat sampah di tiap baris daftar (hanya pemilik/admin) dan File → "Move to Trash…" di editor, keduanya dengan dialog konfirmasi; filter **Trash** di daftar
  (state di URL `?scope=trash`) dengan tombol "Restore Diagram". Aturan Web Interface Guidelines: tombol ikon ber-`aria-label`, aksi destruktif tidak instan (dialog + Trash sebagai undo).

## Consequences
- Tidak ada penghapusan permanen: baris, op log, versi, dan objek S3 tetap ada. Ini sengaja (audit/DR). Pembersihan permanen (hard delete + retensi) belum ada dan perlu keputusan
  terpisah (berapa lama, siapa yang boleh, penghapusan objek S3).
- Dokumen di Trash tidak bisa dibuka, di-share, atau diedit sampai dipulihkan (semua jalur memakai `deleted_at IS NULL`); token MCP dokumen itu berhenti berfungsi selama di Trash.
- Tidak ada perubahan skema (`schema_version` tetap 3); cukup image baru.

## Alternatives
- Hard delete langsung — ditolak: tidak bisa dibalik, bertentangan dengan jejak audit.
- Konfirmasi saja tanpa Trash — ditolak: tidak ada undo untuk salah hapus yang sudah dikonfirmasi.

## Evidence
`internal/platform/trash_it_test.go` (`TestIT_TrashAndRestore`: editor 403, stranger 404, owner/admin hapus+pulihkan, AI ditolak, share bertahan, restore dua kali = 404)
lulus terhadap PostgreSQL/Redis/SeaweedFS lab (2026-11-04). Gerbang lokal: build, vet, `go test ./internal/...`, golangci-lint 0 isu.

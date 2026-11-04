# ADR-0019 — Hapus hanya oleh pemilik, "All" tanpa Trash, tombol hapus di dalam card, chip Shared

Author / Agent: Claude Code
Timestamp: 2026-11-05
Status: **Accepted (2026-11-05, permintaan user)** — merevisi ADR-0018 (hak hapus/pulihkan) dan melengkapi ADR-0004 (PDP).

## Decision
- **Hak hapus dan pulihkan = pemilik saja.** Aksi `delete` di PDP tidak lagi memberi hak ke admin aplikasi (admin tetap boleh baca, share, policy, audit). Editor, viewer, admin, dan aktor AI mendapat 403 (admin: 403 saat DELETE, 404 saat undelete).
- **Trash milik pemilik**: `GET /api/v1/trash` hanya memuat diagram milik pemanggil (admin tidak lagi melihat semua).
- **Filter**: *All* = milik saya + dibagikan ke saya (tanpa Trash; admin tetap melihat semua diagram seperti sebelumnya); *Owned by me*; *Shared with me* (bukan milik saya dan punya role); *Trash* = hanya soft delete milik saya.
- **Diagram yang dihapus pemilik langsung hilang dari daftar penerima share** (bukan "trashed" di sana); share tetap tersimpan dan kembali saat dipulihkan.
- **UI**: tombol ikon hapus berada di dalam card yang sama dengan judul (kanan), tidak lagi elemen terpisah; seluruh card tetap bisa diklik lewat tautan judul. Tombol Restore di Trash juga di dalam card.
- **Chip kuning "Shared · N"** di card milik sendiri bila diagram dibagikan; klik membuka dialog berisi tiap penerima dan role-nya (`*` ditampilkan "Everyone"). Daftar dikirim oleh `GET /api/v1/documents` sebagai `sharedWith` (hanya untuk pemilik dan admin; penerima tidak melihat siapa lagi yang punya akses).

## Consequences
- Tanpa DDL/env/Helm; hanya image baru (platform + gateway karena aset web).
- Admin tidak bisa lagi memulihkan diagram orang lain dari Trash (konsekuensi sadar; bila perlu moderasi, dibuat jalur admin terpisah).
- `ListDocuments` menjalankan satu query tambahan (`permissions` untuk id di halaman, maks 500) untuk `sharedWith`.

## Evidence
`internal/platform/trash_it_test.go` (diperbarui: admin DELETE 403, trash admin kosong, sharee kehilangan diagram, `sharedWith` hanya untuk pemilik) dan `e2e/phase12_trash.mjs` (chip, dialog, filter). Build/vet/lint/`go test ./internal/...` hijau lokal; IT dan E2E menunggu deploy lab.

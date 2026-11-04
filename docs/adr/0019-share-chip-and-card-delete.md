# ADR-0019 — "All" tanpa Trash, tombol hapus di dalam card, chip Shared

Author / Agent: Claude Code
Timestamp: 2026-11-05
Status: **Accepted (2026-11-05, permintaan user)** — melengkapi ADR-0018 dan ADR-0004 (PDP).

## Decision
- **Hak hapus dan pulihkan = pemilik atau admin aplikasi** (tidak berubah dari ADR-0018; sempat dipersempit ke pemilik saja pada 2026-11-05 lalu dikembalikan atas permintaan user di hari yang sama). Editor, viewer, dan aktor AI mendapat 403/404.
- **Trash**: `GET /api/v1/trash` memuat diagram milik pemanggil; admin melihat semua (sehingga dapat memulihkan diagram orang lain).
- **Filter**: *All* = milik saya + dibagikan ke saya (tanpa Trash; admin tetap melihat semua diagram seperti sebelumnya); *Owned by me*; *Shared with me* (bukan milik saya dan punya role); *Trash* = soft delete milik saya (admin: semua).
- **Diagram yang dihapus pemilik langsung hilang dari daftar penerima share** (bukan "trashed" di sana); share tetap tersimpan dan kembali saat dipulihkan.
- **UI**: tombol ikon hapus (pemilik atau admin) berada di dalam card yang sama dengan judul (kanan), tidak lagi elemen terpisah; seluruh card tetap bisa diklik lewat tautan judul. Tombol Restore di Trash juga di dalam card.
- **Chip kuning "Shared · N"** di card milik sendiri bila diagram dibagikan; klik membuka dialog berisi tiap penerima dan role-nya (`*` ditampilkan "Everyone"). Daftar dikirim oleh `GET /api/v1/documents` sebagai `sharedWith` (hanya untuk pemilik dan admin; penerima tidak melihat siapa lagi yang punya akses).

## Consequences
- Tanpa DDL/env/Helm; hanya image baru (platform + gateway karena aset web).
- `ListDocuments` menjalankan satu query tambahan (`permissions` untuk id di halaman, maks 500) untuk `sharedWith`.

## Evidence
`internal/platform/trash_it_test.go` (diperbarui: admin tetap bisa hapus/pulihkan, sharee kehilangan diagram, `sharedWith` hanya untuk pemilik) dan `e2e/phase12_trash.mjs` (chip, dialog, filter). Build/vet/lint/`go test ./internal/...` hijau lokal; IT dan E2E menunggu deploy lab.

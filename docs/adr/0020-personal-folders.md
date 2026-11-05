# ADR-0020 — Folder Pribadi & Penataan Diagram per Pengguna (Personal View)

Author / Agent: Claude Code
Timestamp: 2026-11-05
Status: **Accepted (2026-11-05)** — melengkapi ADR-0004 (PDP), ADR-0016 (MCP/AI), ADR-0018, dan ADR-0019.

## Context
Seiring bertambahnya diagram milik sendiri dan diagram yang dibagikan oleh rekan kerja, tampilan datar (flat list) di daftar utama menyulitkan pengorganisasian. Pengguna membutuhkan struktur folder untuk merapikan workspace diagram mereka. Namun demikian, kepemilikan dokumen dan hak akses (ACL) tidak boleh terganggu oleh penataan folder pribadi masing-masing pengguna.

## Decision
- **Folder dan penataan diagram per-pengguna adalah personal VIEW, bukan bagian dari ACL**:
  Tabel `folders` dan `doc_placement` (migrasi `004_folders.sql`, skema `schema_version` 4). Menata diagram ke dalam folder pribadi tidak pernah mengubah hak akses diagram (siapa yang boleh membaca, mengedit, atau menghapus). PDP dan ACL tetap mengacu pada tabel `documents` dan `permissions`.
- **Penempatan default tanpa baris `doc_placement`**:
  Diagram tanpa baris di `doc_placement` berada di root bagi diagram milik sendiri, atau di dalam folder virtual **"Shared with me"** bagi diagram yang dibagikan oleh orang lain. Baris `doc_placement` dengan `folder_id` NULL merepresentasikan penempatan eksplisit di root.
- **Penerima share dapat mengarsip ke folder pribadi sendiri**:
  Penerima diagram yang dibagikan dapat memindahkan diagram tersebut ke dalam folder buatannya sendiri tanpa memengaruhi penataan atau tampilan pada pemilik diagram maupun pengguna lain.
- **Hapus folder memindahkan isi ke parent**:
  Menghapus folder (`DELETE /api/v1/folders/{id}`) tidak pernah menghapus diagram atau subfolder di dalamnya. Seluruh subfolder dan diagram di dalam folder yang dihapus secara otomatis dinaikkan ke parent folder (atau ke root bila parent-nya adalah root). Jika pemindahan subfolder ke atas menimbulkan konflik nama yang sama di level parent, server menolak dengan 409 Conflict agar pengguna mengubah nama terlebih dahulu.
- **Batasan & validasi**:
  - Kedalaman nesting folder maksimal 8 tingkat di bawah root (`maxFolderDepth = 8`).
  - Maksimal 500 folder per pengguna (`maxFolders = 500`).
  - Panjang nama folder 1..100 karakter, tanpa karakter kontrol, serta tidak boleh mengandung karakter slash (`/`) atau backslash (`\`).
  - Nama folder harus unik per parent secara case-insensitive (`UNIQUE INDEX folders_unique_name ON folders (owner, COALESCE(parent_id, ''), lower(name))`).
- **Khusus manusia (Human-only)**:
  Fitur folder hanya dapat digunakan oleh pengguna manusia yang terautentikasi. Aktor AI dan token MCP ditolak dengan status HTTP 403 Forbidden.
- **Folder sharing di luar cakupan**:
  Berbagi seluruh folder (folder-level permissions / inherited ACL) secara eksplisit berada di luar cakupan saat ini (potensial untuk Phase 2 berikutnya).
- **Daftar endpoint API**:
  - `GET /api/v1/folders`: mengambil seluruh hierarki folder milik pemanggil.
  - `POST /api/v1/folders`: membuat folder baru (`{ name: string, parentId?: string | null }`).
  - `PATCH /api/v1/folders/{id}`: mengubah nama dan/atau memindahkan folder (`{ name?: string, parentId?: string | null }`).
  - `DELETE /api/v1/folders/{id}`: menghapus folder; isi dipindahkan ke parent folder.
  - `PUT /api/v1/documents/{id}/placement`: menata lokasi diagram bagi pemanggil (`{ folderId: string | null, inbox: boolean }`). Parameter `inbox: true` mengembalikan diagram ke lokasi bawaan (root untuk diagram sendiri, "Shared with me" untuk diagram orang lain).

## Consequences
- Memerlukan eksekusi migrasi DDL `internal/platform/migrations/004_folders.sql` (schema_version 4) sebelum service platform versi baru dijalankan (service menolak beroperasi bila schema_version < 4).
- Penataan bersifat independen per pengguna; dua pengguna yang memiliki akses ke diagram yang sama dapat menaruhnya di struktur folder yang sepenuhnya berbeda.
- UI daftar diagram mendukung navigasi hierarkis, breadcrumb, parameter URL `?folder={id}`, drag-and-drop dokumen ke breadcrumb, dan dialog modal "Move to folder…".

## Evidence
- DDL migrasi: `internal/platform/migrations/004_folders.sql` (tabel `folders`, `doc_placement`, indeks, dan bump `schema_version` ke 4).
- Implementasi API dan penyimpanan: `internal/platform/api_folders.go`, `internal/platform/store_folders.go`, dan unit/IT test `internal/platform/folders_it_test.go`.
- Pengujian E2E browser: `e2e/phase13_folders.mjs` (pembuatan folder & subfolder, penolakan 409 duplicate name, penolakan 400 move child cycle, isolasi antar-pengguna 404, penataan diagram via modal dialog & drag-to-breadcrumb, penataan diagram shared oleh recipient, pencarian lintas folder, dan penghapusan folder dengan migrasi isi ke parent).

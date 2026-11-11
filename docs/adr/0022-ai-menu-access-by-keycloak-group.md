# ADR-0022 — Menu AI/MCP hanya untuk anggota grup Keycloak

Author / Agent: Claude Code
Timestamp: 2026-11-10
Status: **Accepted untuk kantor (2026-11-10, diminta user)** — menggantikan "menu AI disembunyikan untuk semua" (commit `3d60ed8`); melengkapi ADR-0016 (token MCP per diagram) dan ADR-0009 (AI sebagai kolaborator). Belum dibawa ke lab (lab: `AI_ACCESS_ROLE` kosong = semua boleh, perilaku lama).

## Context
Sejak 2026-11-10 menu AI (MCP access) disembunyikan untuk semua orang, termasuk admin, sampai ada cara memilih siapa yang boleh. User ingin pemilihan itu dikelola di **Keycloak** lewat grup (mis. `mcp-access`), bukan di halaman admin DrawSpace: orang di grup itu melihat menu AI dan memakainya seperti biasa.

## Decision
1. **Satu aturan bersama**: `config.AIAccess(enabled, requiredRole, userRoles)`. `enabled` = saklar global `AI_ENABLED`; `requiredRole` = client role `AI_ACCESS_ROLE` (mis. `mcp-access`). Bila saklar mati: tidak ada yang boleh. Bila `AI_ACCESS_ROLE` kosong: semua pengguna yang login boleh (lab). Bila terisi: harus memegang role itu (cocok persis). **Admin tidak mendapat jalan pintas**; admin yang perlu AI masuk ke grup.
2. **Sumber kebenaran = Keycloak.** Client role `mcp-access` pada client DrawSpace (`drawio-dev`); grup `/drawio/mcp-access` yang memetakan role itu. Anggota grup menerima role di token (`resource_access.<client>.roles`) — jalur yang sama dengan `drawio-admin` (`/drawio/drawio-admin`). Tidak memakai klaim `groups`.
3. **Penegakan di server**: platform menolak `POST /documents/{id}/mcp-tokens` tanpa role (`403 ai_not_permitted`, audit `mcp-token-create` denied "no AI access role"), sebelum apa pun tentang diagram terungkap. Listing dan pencabutan token tetap milik pemilik (dan admin sebagai saklar darurat) supaya token selalu bisa dibersihkan.
4. **UI hanya kosmetik**: gateway menambah `features.ai` di `GET /api/me` (dihitung dari `AI_ENABLED` + `AI_ACCESS_ROLE` + role session). `FEATURES.ai` di `host.js` mengikutinya: menu AI, chip "AI allowed", dan panel MCP access hanya muncul untuk anggota grup.
5. **Konfigurasi dari manifest**: `AI_ENABLED` dan `AI_ACCESS_ROLE` di **gateway dan platform** dengan nilai sama (`values-env-dev.yaml`). mcp-server tidak butuh (ia bertanya ke platform).
6. **Skrip Keycloak** `scripts/keycloak_mcp_access.py` (`--check`/`--apply`/`--verify`/`--undo`): hanya menambah role, grup, dan pemetaan; tidak menyentuh client/flow/grup lain atau keanggotaan; password admin diketik di terminal.

## Consequences
- Perubahan keanggotaan grup berlaku setelah **login ulang** (role disimpan di session: 8 jam, idle 30 menit).
- **Batas yang diketahui (opsi A)**: role hanya diperiksa saat token dibuat. Token yang sudah ada tetap berfungsi sampai kedaluwarsa (default 30 hari) atau dicabut walau pemiliknya kemudian keluar dari grup. Mitigasi: cabut token pemilik itu (UI pemilik/admin atau SQL `UPDATE document_tokens SET revoked_at=now() WHERE created_by=…`), atau matikan semuanya dengan `AI_ENABLED=false`. **Tindak lanjut (opsi B, belum dibangun)**: periksa role pemilik token juga saat token dipakai (`/internal/mcp-tokens/resolve`) lewat pencarian ke Keycloak melalui gateway dengan cache ~60 detik; butuh kanal platform → gateway yang bertanda tangan (jalur kepercayaan baru).
- Tidak ada DDL. Urutan rollout: role+grup di Keycloak → anggota pilot → env di manifest → deploy image.

## Alternatives
- Klaim `groups` di token (Group Membership mapper) — ditolak: perlu mapper pada client, jalur role sudah terbukti untuk `drawio-admin`.
- Allow list di tabel DrawSpace yang diisi admin — ditolak oleh user: pengelolaan akses harus di Keycloak.
- Admin otomatis boleh — ditolak: sesuai permintaan user, hanya anggota grup.

## Evidence
Unit: `internal/config/aiaccess_test.go`, `internal/edge/features_test.go`; skrip Keycloak diuji terhadap mock lokal (check, apply, apply ulang tanpa tulisan baru, verify, undo menjaga grup berisi anggota). Belum diuji terhadap Keycloak kantor.

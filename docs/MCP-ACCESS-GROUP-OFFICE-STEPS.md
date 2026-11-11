# Menu AI/MCP lewat grup Keycloak — langkah kantor

Author / Agent: Claude Code
Timestamp: 2026-11-10
Rujukan: ADR-0022 (`docs/adr/0022-ai-menu-access-by-keycloak-group.md`). Tanpa DDL. Hanya Anda yang menjalankan perintah di bawah (password admin diketik sendiri; jangan ditempel ke chat).

## Urutan (wajib)
1. Keycloak: buat role + grup (langkah 1) dan masukkan akun pilot (langkah 2).
2. Manifest: env gateway + platform (langkah 3), commit/push `platform-manifest`.
3. Deploy image baru (build Jenkins `draw-space` → bump tag+digest → ArgoCD). Sebelum image baru, env baru tidak berpengaruh (aman).
4. Uji (langkah 5).

## 1. Keycloak: role + grup (dari Mac, auth.dev.btn.co.id terjangkau langsung)
```bash
cd /Users/macbook/Documents/custom-apps/draw-space
# baca saja: apa yang ada / akan dibuat
python3 -I scripts/keycloak_mcp_access.py --check
# buat yang kurang (hanya menambah; ketik username+password admin saat diminta)
python3 -I scripts/keycloak_mcp_access.py --apply
```
Default: `KC_URL=https://auth.dev.btn.co.id`, `KC_REALM=platform-dev-btn`, `KC_CLIENT=drawio-dev`, role `mcp-access`, grup `/drawio/mcp-access`. Admin realm `master` (ubah dengan `KC_ADMIN_REALM` bila admin Anda di realm lain). Bila `--check` menulis "Full scope allowed: off ... NO", tambahkan role itu di Clients > drawio-dev > Client scopes > drawio-dev-dedicated > Scope (skrip tidak mengubah client).

Manual (tanpa skrip): Clients > `drawio-dev` > Roles > Create role `mcp-access`; Groups > `drawio` > Create child group `mcp-access`; grup itu > Role mapping > Assign role > Filter by clients > `drawio-dev` > `mcp-access`.

## 2. Anggota
Groups > `/drawio/mcp-access` > Members > Add member (atau sinkron dari AD bila ada group mapper). Verifikasi:
```bash
python3 -I scripts/keycloak_mcp_access.py --verify <username>     # "effectively holds role mcp-access: YES"
```
Orang itu **login ulang** agar role masuk session.

## 3. Manifest (repo `helm-values/platform-manifest`), gateway DAN platform dengan nilai sama
`drawio-custom-service/gateway/values-env-dev.yaml` dan `drawio-custom-service/platform/values-env-dev.yaml`, di bawah `container.env`:
```yaml
    AI_ENABLED: "true"
    AI_ACCESS_ROLE: mcp-access
```
mcp-server tidak perlu env ini. Secret tidak berubah.

## 4. Deploy
Push source `draw-space` (setelah persetujuan), tunggu Jenkins hijau, ArgoCD Synced/Healthy untuk gateway + platform.

## 5. Uji
- User anggota grup: menu **AI** muncul → MCP access… → buat token → `Authorization: Bearer dsp_…` ke `/mcp` bekerja.
- User bukan anggota: tidak ada menu AI; `POST /api/v1/documents/<id>/mcp-tokens` = 403 `ai_not_permitted`.
- Admin (`drawio-admin`) yang bukan anggota: juga tidak melihat menu (sesuai keputusan).
- Audit: percobaan ditolak tercatat `mcp-token-create` denied "no AI access role".

## Undo
- Matikan cepat tanpa Keycloak: `AI_ENABLED: "false"` (atau hapus) di dua values → semua token ditolak lagi.
- Cabut akses seseorang: keluarkan dari grup (berlaku untuk token BARU setelah login ulang; token lama: cabut di panel pemilik atau `UPDATE document_tokens SET revoked_at=now() WHERE created_by='<username>' AND revoked_at IS NULL;`).
- Hapus objek Keycloak: `python3 -I scripts/keycloak_mcp_access.py --undo` (menjaga grup yang masih punya anggota).

## Status
| Item | Status |
|---|---|
| Kode + tes unit (config, gateway `/api/me`) | ✅ commit lokal `52e1154` (2026-11-10), sudah push (`f2583ee`) |
| Skrip Keycloak diuji terhadap mock | ✅ |
| Keycloak kantor: role + grup + pemetaan | ✅ 2026-11-10 (`--check` lalu `--apply`, output tmux) |
| Keycloak kantor: anggota grup (akun pilot `galih22598`) | ✅ 2026-11-10, `--verify` = YES |
| Manifest env gateway+platform | ✅ push `platform-manifest` `c11e13f` (2026-11-10) |
| Deploy kantor (image `f2583ee2` + env) dan menu AI muncul untuk `galih22598` | ✅ 2026-11-10 (konfirmasi user) |
| Uji akun non-anggota (menu tidak muncul, API 403) | 🟡 belum diuji |
| Backport ke lab (`lab/drawio-custom-service`) | 🟡 |
| Opsi B (cek role pemilik saat token dipakai) | ⬜ belum dibangun |

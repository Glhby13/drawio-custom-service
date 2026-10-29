# ADR-0016 — Token MCP per diagram (menggantikan OAuth Keycloak untuk MCP)

Author / Agent: Claude Code
Timestamp: 2026-10-29 23:00
Status: **Accepted untuk lab (2026-10-29)** — menambah ADR-0008 dan ADR-0009 (keduanya tidak diubah); jalur OAuth Keycloak di ADR-0008 tetap ada di kode tetapi **mati secara default**

## Context
MCP lewat OAuth Keycloak (ADR-0008) membutuhkan satu client Keycloak per aplikasi agen dan agen yang login sebagai user. Untuk kebutuhan
"tempel satu token di konfigurasi Claude lalu langsung bisa" itu terlalu berat. Model yang diminta mirip API key MCP n8n, dengan perbedaan
penting: token berlaku untuk **satu diagram**, bukan seluruh akun, dan hanya **pemilik** diagram yang boleh membuatnya.

## Decision
- **Token = capability per dokumen.** Bentuk `dsp_<32 byte acak, base64url>` (prefix agar pemindai secret mengenalinya). Hanya SHA-256 yang disimpan
  (tabel `document_tokens`, migrasi 003, `schema_version` 3); token ditampilkan **sekali** saat dibuat. Atribut: nama, 4 karakter terakhir (hint), peran
  (`viewer`/`commenter`/`editor`), pembuat, kedaluwarsa (opsional, maks. 365 hari; UI default 30 hari), terakhir dipakai, dicabut.
- **Hanya pemilik** membuat dan melihat daftar token (`POST|GET /api/v1/documents/{id}/mcp-tokens`). Aktor AI, editor, dan administrator aplikasi tidak
  bisa membuat. Mencabut (`DELETE …/{tid}`) boleh pemilik, dan administrator aplikasi sebagai saklar darurat. Maks. 20 token hidup per diagram.
- **Membuat token menyalakan "Allow AI agents"** bila belum aktif (`enableAI`, UI selalu mengirimnya; API tanpa flag menjawab 409 `ai_disabled`).
- **Jalur MCP**: `Authorization: Bearer dsp_…` ke `POST /mcp`. mcp-server tidak punya kredensial database (ADR-0008), jadi ia bertanya ke platform di
  **setiap request** (`POST /api/v1/internal/mcp-tokens/resolve`, hanya dengan service token `drawio-mcp`/`mcp-resolver`): pencabutan, kedaluwarsa, atau
  mematikan "Allow AI" berlaku pada panggilan berikutnya. Jawaban: `{tokenId, documentId, role, owner}`.
- **Aktor**: `ai-<nama token>` atas nama **pemilik** (`onBehalfOf`), `agent = token:<id>`. mcp-server menandatangani token internal dengan klaim
  `docScope`/`docRole`/`tokenId`; platform hanya mempercayainya bila penerbitnya `drawio-mcp` (token dari issuer lain dengan klaim yang sama diabaikan).
- **Izin efektif (PDP)**: satu dokumen saja (`docScope`), peran = min(peran token, editor), hanya bila `aiPolicy.allowed` dan pembuat token masih pemilik.
  Batas ops/menit dan `maxDelete` per dokumen tetap berlaku. Aktor token tidak bisa membuat diagram, berbagi, menghapus, mengatur kebijakan, atau membuat token.
- **Tool**: `create_diagram` tidak ditawarkan (dan ditolak bila dipanggil); parameter `document` opsional (default = diagram token); diagram lain ditolak
  sebelum menyentuh platform. `list_diagrams` hanya memuat diagram token.
- **OAuth Keycloak untuk MCP dimatikan** secara default (`MCP_OAUTH_ENABLED=true` menyalakannya kembali; env `ISSUER_URL`, `MCP_AUDIENCE`, `APP_CLIENT_ID` hanya
  dibaca bila menyala). Metadata RFC 9728 tidak lagi disajikan saat mati. Keycloak tetap dipakai untuk login **manusia** ke DrawSpace.
- **UI**: bagian "MCP access for AI agents" di dialog Share (hanya pemilik): daftar token, buat (nama, peran, kedaluwarsa), salin token dan perintah
  `claude mcp add --transport http …`, cabut dengan konfirmasi satu kali.
- **Audit**: `mcp-token-create`, `mcp-token-revoke`, penolakan (`denied`) pembuatan/pencabutan, dan `mcp-token-use` `denied` untuk token yang ditolak;
  perubahan isi tercatat di op log dengan aktor `ai-<nama>` dan agen `token:<id>`.

## Consequences
- Pemegang token = pemegang akses (bearer): kebocoran dibatasi satu diagram, peran, dan kedaluwarsa, dan bisa dicabut seketika. Token tidak boleh masuk
  URL/log; hanya header. TLS wajib (ingress).
- Tanpa Keycloak, agen tidak punya identitas user; akuntabilitas = pemilik pembuat token + nama/id token di audit.
- Agen tidak bisa membuat diagram atau melihat banyak diagram lewat MCP (token "semua diagram saya" sengaja tidak ada).
- Setiap panggilan MCP = satu lookup indeks di PostgreSQL (hash unik). Batas yang diketahui: tidak ada rate limit per IP untuk token tak dikenal di mcp-server
  (token 256-bit tak bisa ditebak; risikonya hanya beban lookup) — pekerjaan lanjutan bila diperlukan.
- Skrip `scripts/setup_keycloak_mcp.py` dan suite `e2e/phase6_mcp.mjs` hanya relevan bila OAuth dinyalakan; jalur token diuji `e2e/phase10_mcp_token.mjs`
  dan `internal/platform/tokens_it_test.go`.

## Alternatives
- Token "semua diagram saya" per user (seperti API key n8n) — ditolak: jejak kebocoran lebih besar, bertentangan dengan permintaan token unik per diagram.
- Menyimpan token mentah / terenkripsi — ditolak: hash cukup (256 bit acak) dan tidak ada kebutuhan menampilkan ulang.
- Memvalidasi token di mcp-server dengan cache lokal — ditolak untuk sekarang: pencabutan harus seketika; lookup per request murah.
- Mengubah ADR-0008/0009 — ditolak: ADR baru menambah, riwayat keputusan tetap utuh.

## Evidence
Unit: `internal/mcpsrv/token_test.go`, `internal/platform/tokens_test.go`. Integrasi terhadap PostgreSQL/Redis/SeaweedFS lab: `TestIT_MCPTokens` (lulus 2026-10-29,
database `drawio_test`). E2E Chrome `e2e/phase10_mcp_token.mjs` ditulis; dijalankan setelah deploy.

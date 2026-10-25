# ADR-0008 — MCP server

Author / Agent: Claude Code
Timestamp: 2026-10-25 01:15
Status: **Accepted untuk lab (2026-10-25)** — diimplementasi di `internal/mcpsrv`; daftar tool di bawah adalah kontrak v1

## Context
Agen AI dan alat lain perlu membaca dan mengubah diagram lewat Model Context Protocol, dengan aturan izin dan audit yang sama
seperti manusia. MCP tidak boleh menjadi jalur belakang ke database/S3.

## Decision
- Deployable terpisah `mcp-server` (ADR-0005): transport **Streamable HTTP**, endpoint di belakang gateway (mis. `https://drawio.lab.local/mcp`)
  tetapi dengan autentikasi sendiri.
- **Keamanan**: OAuth 2.1 dengan Keycloak sebagai authorization server; MCP server = resource server yang menerbitkan
  *Protected Resource Metadata* (RFC 9728) menunjuk issuer Keycloak; token wajib `aud` khusus MCP (mis. `drawio-mcp`), divalidasi via JWKS;
  scope kasar (`diagrams:read`, `diagrams:write`); izin halus tetap di PDP domain (ADR-0004). Rate limit per klien/token, batas ukuran batch,
  audit setiap pemanggilan tool (tool, argumen ringkas, hasil, operationId terkait).
- **Tanpa jalur belakang**: mcp-server hanya memanggil API domain internal (token internal dengan `actorType` dan delegasi) —
  tidak punya kredensial PostgreSQL/S3/Redis.

### Candidate tools (bukan final)
| Tool | Lapisan | Catatan |
|---|---|---|
| `list_diagrams(query?, folder?)` | read | difilter PDP |
| `get_diagram(documentId, view=summary|cells|xml)` | read | ringkasan semantik (page, cell, label, edge, container) dari model Go; XML hanya bila diminta |
| `create_diagram(title, folder?, template?)` | domain | |
| `apply_operations(documentId, baseServerSequence, operations[])` | **primitif** | Semantic Operation Contract (ADR-0010), batch atomik |
| `add_shape(...)`, `connect(...)` | **helper** | dibangun di atas `apply_operations` (membuat op `addCell`/`connect`), tidak punya logika sendiri |
| `layout(documentId, algorithm, scope)` | helper | hasil = op `setGeometry` batch; algoritma layout server-side = UNKNOWN (draw.io layout ada di JS) |
| `export(documentId, format=svg|png|pdf)` | read | export server-side tidak ada di image draw.io (`EXPORT_URL=null`, SPIKE-000 #10) → UNKNOWN, mungkin hanya `xml`/`svg` dari snapshot |
| `list_versions`, `restore_version` | domain | restore = op invers/snapshot sebagai op baru (ADR-0010) |
| `comment(documentId, cellId?, text)` | domain | membutuhkan model komentar (belum didesain) |
| Resource `diagram://{documentId}` | read | |

Prinsip: **MCP → domain operation API**. Helper hanyalah pembungkus yang menghasilkan semantic op; semua mutasi melewati
`authorize → validate → sequence → oplog → broadcast` yang sama dengan klien browser, sehingga perubahan agen muncul realtime di editor user.

## Consequences
- MCP bergantung pada kematangan ADR-0010 dan model dokumen Go (R2-1) — Phase 6 setelah Phase 5.
- Read model untuk agen (ringkasan semantik) harus dirancang agar tidak bocor data yang tidak boleh dibaca (difilter PDP per dokumen).
- Spesifikasi MCP berkembang; versi protokol yang didukung dipin dan diuji (UNKNOWN: versi spesifikasi saat implementasi).

## Alternatives
- MCP mengakses DB/S3 langsung — ditolak (jalur belakang, melewati authz/audit).
- MCP memakai patch native draw.io — ditolak (blob, tanpa niat, sulit dibaca agen; ADR-0010).
- Tanpa MCP, hanya REST — ditolak sebagai tujuan (MCP adalah kebutuhan first-class), REST tetap ada untuk UI.

## Evidence
INFERENCE (desain). E7: patch yang dibangun server berhasil diterapkan ke editor → op dari MCP bisa ditampilkan realtime setelah lowering.

## Addendum 2026-10-25 — implementasi
- Transport: Streamable HTTP, `POST /mcp` JSON-RPC 2.0 (tanpa stream server-initiated; `GET /mcp` = 405), protokol 2025-06-18 / 2025-03-26 / 2024-11-05. Ingress langsung ke service `drawio-mcp` untuk `/mcp` dan `/.well-known/oauth-protected-resource` (gateway tidak ikut jalur ini).
- Auth: bearer access token Keycloak (JWKS, issuer, exp, `aud` = `drawio-mcp`); peran aplikasi dari `resource_access.drawio-lab`; 401 + `WWW-Authenticate: Bearer resource_metadata=…` (RFC 9728).
- Tool v1: `list_diagrams get_diagram create_diagram add_shape connect update_cell delete_cells apply_operations layout export get_history list_versions save_version restore_version comment` + resources `diagram://<id>`. Semua lewat REST platform yang sama dengan UI (PDP, op log, audit). Server platform mengisi `order` bila kosong (`Doc.FillOrders`) dan menyediakan `POST /documents/{id}/layout`.
- Klien agen = satu client Keycloak per aplikasi agen (`drawio-mcp-<nama>` → aktor `ai-<nama>`), skrip `scripts/setup_keycloak_mcp.py`.

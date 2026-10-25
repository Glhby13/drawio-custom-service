# ADR-0001 — Ekstensi draw.io tanpa mengubah draw.io

Author / Agent: Claude Code
Timestamp: 2026-10-25 00:05
Status: Accepted

## Context
draw.io Community menjadi editor/UI. Kemampuan enterprise (SSO, penyimpanan terpusat, izin, versi, kolaborasi, audit, MCP, AI)
dibangun di `drawio-custom-service`. Fork atau patch image akan menjadikan setiap upgrade (rilis draw.io sangat sering:
32.0.2 → 32.6.1 dalam 18 hari, EVIDENCE Docker Hub) pekerjaan merge, dan melanggar prinsip proyek.

Titik integrasi yang tersedia di build self-hosted v32.6.1:

| Titik | Status resmi | Bukti |
|---|---|---|
| Env image → `PreConfig.js` (`DRAWIO_CONFIG`, `DRAWIO_CSP_HEADER`, `DRAWIO_BASE_URL`, …) | resmi (README docker-drawio) | EVIDENCE entrypoint |
| Protokol embed postMessage (`load`, `autosave`, `diffSync`, `patch`, `getDiff`, `resetDiff`, `merge`, `remoteInvoke`, `export`) | **terdokumentasi** di drawio.com embed-mode; format patch "sama dengan format diff internal", tanpa schema formal, tanpa janji stabilitas | EVIDENCE docs + `EditorUi.js` |
| `Draw.loadPlugin(cb)` dipanggil dari host same-origin | komentar sumber: "the only long-term supported solution for access to the EditorUi instance"; halaman plugin resmi: plugin = "unsupported examples" | EVIDENCE `App.js` `initPluginCallback`, docs |
| Plugin via `?p=` | hanya ID di `App.pluginRegistry` bawaan; URL kustom ditolak (`Unknown plugin`) | EVIDENCE runtime E1 |
| Plugin via settings | hanya bila `ALLOW_CUSTOM_PLUGINS=true` (PreConfig) **dan** daftar plugin ada di `localStorage` user | EVIDENCE runtime E1 — tidak praktis |
| `mxEvent.CHANGE` / `mxUndoableEdit` | API internal mxGraph; objek change tidak bisa di-encode `mxCodec` (cyclic reference) | EVIDENCE runtime smoke/E2 |

## Decision
1. Image `jgraph/drawio` dipakai **apa adanya** (tag dipin). Tidak ada fork, patch file di image, atau rebuild WAR.
2. Konfigurasi hanya lewat env resmi (atau `PreConfig.js` dari ConfigMap bila `readOnlyRootFilesystem` diwajibkan — diputuskan di Phase 1).
3. **Batas integrasi utama = protokol embed resmi** (`autosave` + `diffSync`, `patch`, `getDiff`, `resetDiff`, `load`, `export`).
   Halaman editor milik gateway (same-origin) memuat draw.io di iframe `/editor/?embed=1&proto=json…`.
4. Adapter klien (JavaScript milik kita, di host page) menerjemahkan patch native ↔ Semantic Operation (ADR-0010).
5. Akses ke `EditorUi` lewat `Draw.loadPlugin` dari host page hanya untuk kebutuhan yang tidak ada di protokol embed
   (presence/kursor/seleksi remote, membaca state sebelum patch untuk diff per-key). Dibungkus satu modul kecil yang
   punya *contract test* per versi draw.io.
6. `mxEvent.CHANGE` **bukan** batas integrasi (lihat SPIKE-001 §10).

## Consequences
- Upgrade draw.io = ganti tag + jalankan contract test adapter (suite SPIKE-001 dijadikan test di Phase 5).
- Ketergantungan pada format diff internal draw.io (tanpa schema formal) → adapter wajib memvalidasi dan menormalkan,
  dan server tidak pernah menyimpan patch native sebagai format persisten (ADR-0007, ADR-0010).
- Fitur yang butuh perubahan perilaku editor (mis. menyembunyikan menu tertentu) hanya bisa lewat `DRAWIO_CONFIG`/URL param;
  bila tidak ada opsi resmi, fitur tersebut tidak dibuat.
- Realtime bawaan draw.io (Google Drive/OneDrive + cache server jgraph) tidak dipakai; sudah dimatikan image (`sync=manual`).

## Alternatives
- **Fork draw.io / patch `app.min.js`** — ditolak: biaya upgrade, melanggar prinsip.
- **Plugin settings + `ALLOW_CUSTOM_PLUGINS`** — ditolak: butuh state `localStorage` per user, mekanisme "unsupported".
- **Membangun editor sendiri di atas mxGraph** — ditolak: di luar tujuan.

## Evidence
SPIKE-000 §4, §7, §9; SPIKE-001 E0, E1, E2 (`spikes/spike-001/results/`).

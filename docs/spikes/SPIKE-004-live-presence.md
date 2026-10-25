# SPIKE-004 — Live presence & in-progress editing visibility

Author / Agent: Claude Code
Timestamp: 2026-10-25 17:00
Catatan nama: diminta sebagai "SPIKE-003-LIVE-PRESENCE"; nomor 003 sudah dipakai di MASTER-PLAN sebagai nama gate integrasi kolaborasi (Phase 4b, kini lulus lewat E2E), maka dokumen ini = **SPIKE-004** agar rujukan lama tidak ambigu.
Status: **GO (lab)** — P0–P2 terbukti dengan Chrome asli; P3 (caret) sengaja tidak dibangun.

## 1. Pertanyaan dan batas
Pengguna lain tidak hanya melihat hasil setelah commit, tetapi juga **apa yang sedang dikerjakan collaborator** (memilih, mengetik label, menarik, mengubah ukuran, AI sedang menerapkan perubahan), tanpa mencampur state ephemeral dengan state dokumen. Aturan: draw.io tidak di-fork/patch; tidak ada perubahan pada kontrak operasi semantik, op log, skema PostgreSQL, versi/riwayat, MCP, otorisasi AI.

```
 DOCUMENT STATE                         PRESENCE STATE
 semantic operations                    ephemeral WebSocket (act)
   -> PostgreSQL op log  -> history        -> Redis pub/sub (antar-replika)
   durable, versi, revert, audit           -> memori hub (tidak ada DB)
                                           TTL 8 dtk, heartbeat 3 dtk
```
Presence: bukan document state, bukan history/versi, bukan operasi semantik, **bukan lock**, bukan sumber otorisasi, bukan operasi MCP. *Presence informs; the semantic operation layer resolves.*

## 2. Audit extension boundary draw.io (diukur di runtime, bukan diasumsikan)
Probe Playwright pada editor resmi 32.6.1 lewat `EditorUi` yang diinjeksi host (`Draw.loadPlugin`), tanpa patch:

| Kebutuhan | API/event yang terbukti ada | Bukti runtime |
|---|---|---|
| Seleksi | `graph.getSelectionModel()` event `mxEvent.CHANGE`, `getSelectionCells()` | event muncul saat klik; multi-seleksi lewat daftar sel |
| Mulai/selesai edit teks | `mxEvent.START_EDITING`, `EDITING_STARTED`, `EDITING_STOPPED` (property `cell`) | ketiganya terpicu pada double-click / commit |
| Draft teks | `graph.cellEditor.textarea` = elemen `contentEditable` (`mxPlainTextEditor`); `innerText`, event `input` | "Postgre" → `"Postgres"` terbaca per ketikan; **`cellEditor.getCurrentValue()` tidak dipakai** (butuh state, melempar bila dipanggil bebas) |
| Caret | `document.getSelection().anchorOffset` di elemen editor | terbaca, **tidak diimplementasi** (P3, invasif) |
| Drag | `graph.graphHandler.first`, `.cells`, `.currentDx/.currentDy` (piksel layar; dibagi `view.scale`) selama mouse turun; `mxEvent.CELLS_MOVED` baru terpicu saat lepas | `dx=10…80` per langkah; `CELLS_MOVED 80 40` hanya setelah mouse up |
| Resize | `graph.selectionCellsHandler.handlers.visit(...)` → `handler.index != null` + `handler.bounds` (layar) selama menarik handle; `CELLS_RESIZED` setelah lepas | `bounds` 130×70 … 170×100 selama drag |
| Menggambar overlay | `graph.view.getOverlayPane()` (SVG) ikut scroll/zoom | dipakai oleh `presence.js` |
| Batas yang dihormati | **`mxEvent.CHANGE` pada model tidak dipakai** untuk presence; tidak ada patch draw.io | — |

Temuan penting: **Escape di editor teks draw.io menerapkan label (bukan membatalkan)**; `EDITING_STOPPED` membawa `cancel=false`. "Cancel" di sisi kita berarti: editor ditutup tanpa perubahan teks → badge hilang dan **tidak ada operasi** (dibuktikan E2E: seq tidak berubah).
Tidak ada STOP condition yang terkena: semua P0–P2 dapat dibangun dari API yang stabil dan teramati.

## 3. Desain
### 3.1 Kontrak pesan (WebSocket yang sudah ada; tidak ada broker baru)
Browser → server: `{"t":"act","act":{"state","page","cells":[id…],"preview":{…},"draft":"…"}}`.
Server → browser lain: `{"t":"act","act":{"sid","user","name","type","agent","state","page","cells","preview","draft","exp"}}`. Identitas (`sid,user,name,type,agent`) diisi **server dari token**, field serupa dari klien diabaikan (diuji spoofing).
| `state` | arti | data |
|---|---|---|
| `selecting` | memilih sel | `cells` (≤50) |
| `editing` | mengedit label | `cells[0]`, `draft` (teks polos ≤500 rune) |
| `dragging` | menggeser (belum commit) | `cells`, `preview.dx/dy` (satuan diagram) |
| `resizing` | mengubah ukuran | `cells[0]`, `preview.x/y/w/h` (diagram, absolut) |
| `connecting` | menarik ujung edge | `cells[0]` |
| `applying` | **hanya server**, aktor AI sedang menerapkan batch (REST/MCP) | `cells` yang disentuh batch |
| `idle` / `gone` | tidak ada aktivitas / pergi | – |
`exp` (ms, TTL 8 dtk; AI 4 dtk) hanya informatif: klien mengakhiri entri pada **jam lokalnya** (tanpa ketergantungan jam antar-mesin) dan heartbeat 3 dtk menyegarkannya.

### 3.2 Lifecycle
`OFFLINE → ONLINE(join) → IDLE ⇄ SELECTING → {EDITING | DRAGGING | RESIZING | CONNECTING} → (commit/cancel/lepas) → SELECTING/IDLE`. Putus koneksi: server mengirim `gone` saat socket tertutup; browser beku tanpa menutup socket → entri kedaluwarsa di klien lain setelah TTL. Tidak ada state yang disimpan: join baru menerima snapshot aktivitas yang masih hidup dari memori hub. Presence **tidak di-DB-kan, tidak di-audit, tidak masuk history**.

### 3.3 Server
- `internal/platform/activity.go`: `sanitizeAct` (peran, state, jumlah sel, regex id, angka finite/rentang, draft ≤500 rune UTF-8, draft hanya saat `editing`), `handleAct`, `announceAI`.
- Peran: viewer/commenter hanya `selecting` (klaim `editing/dragging/…` diturunkan menjadi `selecting`, preview/draft dibuang); editor+ penuh. Akses dokumen = syarat membuka WebSocket (404 tanpa akses), jadi presence tidak pernah sampai ke yang tidak berhak.
- Rate limit per sesi 30/dtk (burst 60), pelanggaran di-drop diam-diam (best effort); `idle` selalu lolos.
- Antar-replika: `ctlMsg{Kind:"act"}` di Redis pub/sub yang sama; tanpa kunci Redis baru.
- AI: operasi lewat REST/MCP memicu `applying` + sel yang disentuh selama 4 dtk, jujur terhadap state sebenarnya (tidak berpura-pura mengetik).

### 3.4 Browser (`internal/edge/web/presence.js`)
Deteksi lokal dari API di atas (throttle: seleksi 40 ms, drag/resize 50 ms, draft 150 ms, heartbeat 3 dtk, hanya mengirim saat payload berubah). Tampilan di overlay SVG editor: kotak berwarna per pengguna (hash nama → palet 8 warna, cukup untuk ≥5 kolaborator), label "Nama · selected/editing/moving/resizing", hantu putus-putus untuk drag/resize, gelembung teks polos untuk draft, AI = garis putus-putus + awalan "AI". Beberapa orang pada sel yang sama ditumpuk labelnya.

## 4. Hasil (E2E `e2e/phase7_presence.mjs`, 51 cek, Chrome asli, 3 pengguna + 9 soket sintetis)
| Kemampuan | Hasil | Bukti |
|---|---|---|
| selection (P0) | **PASS** | klik/seleksi di A → B dan viewer menggambar kotak + label "Nama · selected"; hilang saat deselect; log tidak berubah |
| editing (P0) | **PASS** | double-click A → "editing" di B; commit → badge hilang + perubahan semantik tiba; "cancel" (editor ditutup tanpa perubahan) → badge hilang, **seq tidak berubah** |
| drag (P1) | **PASS** | saat mouse masih turun B melihat hantu + `dx,dy` hidup; bentuk & log B belum berubah; setelah lepas baru commit |
| resize (P1) | **PASS** | B melihat ukuran sementara (mis. 200×110) sebelum commit |
| draft teks (P2) | **PASS** | B melihat "✎ PostgreSQL" saat diketik; label committed tetap lama; tidak ada op; teks polos (tanpa HTML) |
| caret (P3) | tidak dibangun | terbaca lewat `getSelection()` tetapi invasif; di luar prioritas |
| AI presence | **PASS** | MCP `add_shape` → "AI applying changes" pada sel yang disentuh, otomatis hilang ≤ 4,5 dtk, bukan "editing" |
| concurrent edit | **PASS** | dua orang mengedit sel yang sama: viewer melihat keduanya; hasil akhir satu pemenang identik di semua klien (semantik tak berubah) |
| viewer | **PASS** | klaim "editing" viewer tampil sebagai selection; identitas palsu di pesan diabaikan |
| tanpa akses | **PASS** | soket ke diagram yang tidak dibagikan tidak terbuka (404) |
| lifecycle | **PASS** | browser beku (tanpa heartbeat) hilang di klien lain ≈ 7–9 dtk; tutup browser → hilang segera; join belakangan menerima snapshot |
| performa | **PASS** | transport p50 3 ms / p95 6 ms dengan 12 soket pada diagram, 80/80 terkirim; end-to-end dua Chrome (seleksi → overlay) < 400 ms; 0 tulis DB/op log |

Temuan: (1) "Escape" di editor teks draw.io menerapkan label, bukan membatalkan; (2) `getCurrentValue()` tidak aman dipanggil bebas, pakai `textarea.innerText`; (3) pengukuran latensi lintas tab di Chrome headless terdistorsi throttling tab latar belakang → pengukuran transport memakai satu halaman; (4) tab yang dibuka sebelum deploy tidak punya presence.js: perlu reload sekali setelah rilis.
Tes unit/integrasi: `internal/platform/activity_test.go` (sanitasi, peran, kedaluwarsa), `TestLivePresence` (PG/S3/Redis lab: fan-out, role downgrade, spoof, burst limit, snapshot, AI, op log tidak bergerak).
**Rekomendasi: GO.** Batas: draft terlihat oleh semua yang berhak membaca dokumen; presence hilang saat pod platform restart (sengaja); belum diuji >10 kolaborator manusia sungguhan.

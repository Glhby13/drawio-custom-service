# SPIKE-004 — Live presence & in-progress editing visibility

Author / Agent: Claude Code
Timestamp: 2026-10-25 17:00
Catatan nama: diminta sebagai "SPIKE-003-LIVE-PRESENCE"; nomor 003 sudah dipakai di MASTER-PLAN sebagai nama gate integrasi kolaborasi (Phase 4b, kini lulus lewat E2E), maka dokumen ini = **SPIKE-004** agar rujukan lama tidak ambigu.
Status: **hasil ada di bagian "Hasil"** (diperbarui setelah E2E dari deploy GitOps).

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

## 4. Hasil
(diisi dari E2E; lihat bagian bawah setelah deploy)

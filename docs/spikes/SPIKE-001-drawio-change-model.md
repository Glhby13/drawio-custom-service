# SPIKE-001 — Change model draw.io

Author / Agent: Claude Code
Timestamp: 2026-10-25 00:40 (dikoreksi 2026-10-25 02:55)
Status: **Putaran 1 selesai dan di-reproduksi pada image sungguhan** (arm64+amd64, Chrome Mac + Chromium x86_64). Putaran 2 sebagian dikerjakan di SPIKE-002 (fuzz model Go, checksum, PostgreSQL); integrasi adapter↔model masih terbuka.

> **KOREKSI 2026-10-25 (lihat SPIKE-002 §14–§15):**
> 1. Relay naif (S1) divergen di **10/18** skenario, bukan 9/18 (hitung ulang dari data mentah). S3 15/18 dan S3+repair 18/18 tetap.
> 2. Penjelasan performa di §8 **tidak lengkap**: pada 5.000 cell, update graph draw.io murni = 117 ms (±46% dari 253–338 ms); jalur sync bawaan (clone+hash+diff+XML ≈ 100 ms) ±40%.
> 3. Klaim "undo lokal aman" hanya benar untuk sel yang tidak diubah remote; undo setelah edit remote pada sel yang sama **menimpa** edit remote (E9/U2).
> 4. Seluruh hasil E0–E7 direproduksi pada image `jgraph/drawio:32.6.1` (bukan hanya `draw.war`) di arm64 dan amd64; fungsional identik.

Label: **EVIDENCE** (source code v32.6.1 / dokumentasi resmi / eksperimen yang dijalankan, hasil di `spikes/spike-001/results/`),
**INFERENCE** (penalaran), **UNKNOWN** (belum dibuktikan). Tidak ada klaim "berhasil" tanpa perintah + hasil di bawah.

## 1. Lingkungan dan cara mengulang
| Item | Nilai |
|---|---|
| draw.io | `draw.war` v32.6.1 (rilis 2026-10-23, sha256 `44dd5695…5e42aa`), webapp sama dengan isi image `jgraph/drawio:32.6.1` (SPIKE-000 #2) |
| Penyajian | `spikes/spike-001/server.mjs`: `/editor/*` = webapp tanpa perubahan; `/editor/js/PreConfig.js` = setara keluaran `docker-entrypoint.sh` (CSP meta default, `sync=manual`, integrasi cloud mati); host page same-origin di `/` |
| Browser | Chrome headless 155 via `playwright-core` 1.63.0, profil sementara |
| Mesin | MacBook (Apple Silicon, 14 CPU), Node 26.5, Go 1.27.1 |
| Perintah | `cd spikes/spike-001 && npm install && DRAWIO_WEBAPP=<webapp> node run.mjs` (`ONLY=E0,E1,…` untuk sebagian) |
| Hasil mentah | `results/results-E0-E4-E7.json`, `results/results-E5-E6.json`, `results/results-E5R.json`, `spikes/spike-002-oplog/results.json` |

Keterbatasan harness (berlaku untuk semua angka): semua replika berjalan di satu proses browser (tanpa latensi jaringan nyata),
konkurensi disimulasikan dengan menahan relay patch, hanya 2 klien + 1 replika referensi, skenario deterministik (bukan random/fuzz).

## 2. Model data (§9.1)
| Pertanyaan | Jawaban | Label |
|---|---|---|
| Struktur | `mxfile` → `diagram` (page, id + name) → `mxGraphModel` → `root` → cell `0` (root) → cell `1` (layer default) → cell; cell punya `id`, `parent`, urutan anak (= z-order), `vertex`/`edge`, `source`/`target` (edge), `value` (string label **atau** node XML `UserObject`/`object` berisi atribut kustom), `style` (string `k=v;`), `geometry` (`mxGeometry`, relatif terhadap parent) | EVIDENCE `DiffSync.js getJsonForCell`, E2 |
| ID cell stabil lintas save/load? | Ya untuk cell yang ada di XML: ID dibaca dari XML, patch dan replay memakai ID yang sama (E3 replay identik). ID baru dibuat klien (mis. grup `FqBta3aE-U6RTuoH5RYC-1`, page `NowhyMoj2hJLZilVzSMc`) | EVIDENCE E2/E3 |
| Aman sebagai identity? | **CONDITIONAL**: unik per page (bukan global dokumen); ID bisa dipilih bebas oleh klien (patch insert dengan ID bentrok di-*merge*, bukan ditolak — DiffSync "insert states an identity, it does not create one"). Server wajib memvalidasi format/keunikan dan memakai kunci `(pageId, cellId)` | EVIDENCE `patchPages` komentar + E5 add-add |
| Group/ungroup | Grup = cell baru `style=group` + `parent` anak berubah + **geometry anak diubah menjadi relatif** terhadap grup | EVIDENCE E2 group patch |
| Page | Insert page membawa XML page lengkap (`data`) + `previous`; rename = `{u:{pageId:{name}}}`; select page tidak menghasilkan patch (autosave XML saja) | EVIDENCE E2 |
| Edge | `source`/`target` = ID cell; disconnect = `target: ""` | EVIDENCE E2 |
| Reorder | Urutan anak via `previous` (ID sibling sebelumnya) pada setiap sibling yang posisinya berubah | EVIDENCE E2 toBack |
| Style | Satu string utuh; diff draw.io **tidak** per key (komentar sumber: `// LATER: Split into keys and do fine-grained diff`) | EVIDENCE `diffCell` |
| Atribut kustom | Disimpan di `value` sebagai XML (`<UserObject label="X" owner="alice"/>`); patch membawa `xmlValue` utuh | EVIDENCE E2 attr |

## 3. Event change model (§10) — `mxEvent.CHANGE` / `mxUndoableEdit.changes`
Ditangkap dengan listener pada `graph.model` lewat `EditorUi` yang diperoleh dari `Draw.loadPlugin` (host same-origin).

| Operasi | Change yang muncul (E2) |
|---|---|
| move / resize | `mxGeometryChange` |
| label | `mxValueChange` |
| style | `mxStyleChange` |
| atribut | `mxValueChange` (value XML baru; bukan `mxCellAttributeChange`) |
| add / delete / reparent / reorder | `mxChildChange` |
| connect | `mxChildChange` + `mxTerminalChange` |
| disconnect | `mxTerminalChange` |
| group | `mxGeometryChange` + `mxChildChange` |
| add page / rename / select page | `ChangePage` / `RenamePage` / `SelectPage` (kelas draw.io, bukan mxGraph) |

| # | Pertanyaan | Jawaban | Label |
|---|---|---|---|
| 1 | Bisa ditangkap? | Ya, semua 15 operasi uji | EVIDENCE E2 |
| 2 | Bisa diserialisasi? | **Tidak langsung**: `mxCodec.encode(change)` gagal (`Cyclic reference to Graph/EmbedFile`, `codecXmlBytes: 0`); butuh serializer kustom per kelas (dibuat di `inpage.js`) | EVIDENCE smoke/E2 console |
| 3 | Cukup untuk semantic op? | Ya secara isi (berisi nilai baru dan lama) tetapi perlu pengetahuan kelas internal per jenis | EVIDENCE E2 + INFERENCE |
| 4 | Replay? | Tidak diuji pada level change object (replay diuji pada patch native, §4) | UNKNOWN |
| 5 | Reverse/undo? | Objek change punya `execute()` yang membalik dirinya (dipakai undo manager) — tidak diuji sebagai jalur sinkron | INFERENCE |
| 6 | Ada perubahan yang tidak muncul? | View state (zoom/scroll, page format sebagian) tidak lewat model change; select page muncul sebagai `SelectPage` padahal bukan perubahan dokumen | EVIDENCE E2 (selectPage) + `DiffSync viewStateProperties` |
| 7 | Cukup stabil sebagai batas integrasi? | Tidak: kelas internal mxGraph/draw.io, tanpa dokumentasi, butuh akses objek internal | EVIDENCE docs (plugin = unsupported) |
| 8 | Public/stable API? | Detail implementasi internal | EVIDENCE |

**Suitable as integration boundary: NO** (`mxEvent.CHANGE`). Dipakai hanya sebagai sinyal pelengkap di adapter bila perlu.

## 4. Native diff/patch (§11) — `EditorUi.diffPages` / `patchPages` dan protokol embed `diffSync`
| Pertanyaan | Jawaban | Label |
|---|---|---|
| Tersedia di self-hosted? | Ya, `js/diagramly/DiffSync.js` (3.197 baris) di `app.min.js` v32.6.1 | EVIDENCE |
| Bisa dipakai tanpa plugin? | **Ya, lewat protokol embed resmi**: `load` dengan `autosave:1, diffSync:{patchOnly:true}` → setiap perubahan mengirim event `autosave` berisi `patch` + `checksum` (tanpa XML); action `patch` menerapkan patch remote; `getDiff` / `resetDiff` untuk shadow | EVIDENCE `EditorUi.js` (handler `load`/`patch`/`getDiff`/`resetDiff`) + dokumentasi embed-mode + E2/E3/E4 |
| Format patch | `{i:[page…], r:[pageId…], u:{pageId:{name?, view?, cells:{i:[cellJson…], r:[cellId…], u:{cellId:{parent?, previous?, value?|xmlValue?, style?, geometry?(XML), source?, target?, vertex?, edge?, visible?, collapsed?, custom…}}}}}}` | EVIDENCE E2 |
| Granularitas | Per page → per cell → per **properti**, tetapi `style`, `geometry`, `xmlValue` adalah **blob utuh** | EVIDENCE E2, `diffCell` |
| Ordering | `previous` = ID sibling sebelumnya (cell dan page); patcher membangun ulang urutan dengan "claimant order" | EVIDENCE `getClaimantOrder`, E2 |
| `previous` diperlukan? | Ya untuk z-order/urutan page; tanpa itu posisi tidak bisa direkonstruksi | EVIDENCE |
| Konflik | Nilai absolut → menerapkan dua patch bersamaan = **last applied wins per properti-blob**; insert dengan ID bentrok di-merge di tempat dengan posisi lokal; referensi ke cell yang hilang (parent/terminal) di-drop diam-diam; delete menang atas edit bersamaan | EVIDENCE E5, E7, komentar `patchPages` |
| Butuh patch draw.io? | Tidak | EVIDENCE |
| Ack + checksum | `patch` dibalas event `patch` dengan `checksum` (hash `getHashValueForPages`) dan `checksumMismatch` bila host mengirim checksum harapan | EVIDENCE `EditorUi.js` |
| Validasi sisi server? | Format JSON bisa divalidasi server, tetapi **semantik penerapan hanya ada di JavaScript** (`patchPages`); server Go tidak bisa menghitung hasil/checksum draw.io tanpa mengimplementasi ulang | EVIDENCE (kode JS) + INFERENCE |
| Cocok untuk MCP/AI? | Sebagai **target lowering** ya (patch buatan tangan diterapkan: E7 `handBuilt` insert + rename OK); sebagai bahasa yang dibaca/ditulis agen tidak (blob XML geometry, string style, tanpa niat) | EVIDENCE E7 + INFERENCE |
| Bisa diterjemahkan ke semantic op? | Ya: setiap entri `u/i/r` memetakan ke `setGeometry/setValue/setAttribute/setStyle/reparent/reorder/addCell/deleteCell/connect/addPage/renamePage`; diff per key style/atribut butuh nilai lama dari shadow adapter | EVIDENCE (format) + INFERENCE (penerjemah belum dibuat) |
| Dokumentasi | Embed-mode resmi mendokumentasikan `diffSync`, `patch`, `getDiff`, `resetDiff`, `merge`, `autosave`, `remoteInvoke`; patch "uses the same structure as draw.io's internal diff format", **tanpa schema formal**, cara hitung checksum tidak didokumentasikan, tidak ada janji stabilitas | EVIDENCE docs (dibaca 2026-10-24) |

**Suitable as integration boundary: CONDITIONAL** (protokol embed `diffSync`/`patch`): terdokumentasi dan bekerja, tetapi
format tanpa schema/jaminan → adapter harus memvalidasi dan contract test per versi; **tidak** dijadikan format wire publik atau persistensi.

## 5. Plugin (§12)
| Jalur | Hasil | Label |
|---|---|---|
| Host same-origin memanggil `iframe.contentWindow.Draw.loadPlugin(cb)` | **Berhasil**, `EditorUi` didapat (`attached: true`) | EVIDENCE E1 |
| `?p=/spike-plugin.js` | Ditolak: `Unknown plugin: /spike-plugin.js` (hanya ID registry bawaan) | EVIDENCE E1 + `App.loadPlugins` |
| Settings `plugins` (localStorage), konfigurasi default | Ditolak: `Unknown plugin` | EVIDENCE E1 |
| Settings `plugins` + `window.ALLOW_CUSTOM_PLUGINS = true` di PreConfig | Dimuat | EVIDENCE E1 (butuh PreConfig kustom + localStorage per user → tidak praktis) |
| CSP default image | Plugin/skrip same-origin diizinkan (`script-src 'self'`); fetch same-origin OK; fetch cross-origin diblokir (`connect-src`); WebSocket same-origin tidak memicu violation | EVIDENCE E0 |

**Plugin dapat dimuat tanpa patch image: YES** (host-injected `Draw.loadPlugin`, same-origin). Catatan: jalur ini butuh editor dan host
satu origin (gateway mem-proxy `/editor/`), dan API `EditorUi` tetap internal → dibungkus modul kecil + contract test.

## 6. Remote apply (§13)
Klien B punya 1 edit lokal (label Y), seleksi Z, page p1; patch A (move X) diterapkan ke B lewat action `patch` (E4):

| Pemeriksaan | Hasil | Label |
|---|---|---|
| Echo loop (B mengirim autosave setelah patch remote) | 0 autosave | EVIDENCE E4 `echoesOnApply: 0` (penyebab: `ignoreChange` di handler `patch`) |
| Undo lokal rusak? | Riwayat undo tetap 1, `indexOfNextAdd` tetap; undo di B membatalkan label lokal, **bukan** move remote (X tetap di x=140). **Catatan (E9/U2):** bila remote mengubah *label sel yang sama*, undo lokal mengembalikan nilai lama dan menimpa edit remote | EVIDENCE E4, E9 + `DrawioFile.patch` memulihkan history |
| Duplikat patch | Dikirim 2× → state sama, 0 echo, tanpa error (idempoten untuk nilai absolut) | EVIDENCE E4 `duplicate` |
| Seleksi | Tetap `Z`; bila cell terpilih dihapus remote, seleksi menjadi kosong tanpa error | EVIDENCE E4 |
| Page aktif | Patch ke p1 saat B di p2 → B tetap di p2, 0 echo | EVIDENCE E4 |
| Edit in-place saat patch masuk | Editor tetap terbuka dan teks ketikan tidak hilang untuk patch geometri/label sel lain/label sel sama; saat selesai edit teks lokal menimpa label remote; bila sel dihapus remote, editor menutup tanpa error dan teks hilang | EVIDENCE E8 (SPIKE-002 §13); posisi caret **UNKNOWN** |
| Latensi apply (diagram kecil) | ±40 ms termasuk ack | EVIDENCE E4 |

**Penandaan LOCAL / REMOTE / REPLAY**: draw.io sendiri tidak memberi tanda asal pada autosave. Mekanisme yang terbukti: patch yang masuk
lewat action `patch` tidak pernah menghasilkan autosave (E4), sehingga **semua autosave = LOCAL**. Adapter memberi tanda REMOTE/REPLAY
pada patch yang ia kirim (urutan server + `operationId`) dan mengabaikan ack-nya. EVIDENCE E4 + desain (ADR-0010).

## 7. Matriks konflik (§14)
Strategi: **S1** = relay naif (tiap klien menerapkan patch klien lain). **S3** = urutan server (A dulu, lalu B; offline: B dulu) diterapkan
ke replika R; tiap klien menerapkan entri remote lalu **menerapkan ulang patch miliknya yang belum di-ack**. **S3+repair** = S3, lalu klien
yang checksum-nya beda dari R menghitung `diffPages(milik sendiri, R)` dan menerapkannya. Diharapkan = state R (LWW urutan server).

| Skenario | S1 konvergen | S3 konvergen = R | S3+repair | Lost update? | Hasil R (urutan server) |
|---|---|---|---|---|---|
| move vs move (cell sama) | ✗ | ✓ | — | ya, by design (LWW) | posisi B |
| move vs resize (cell sama, properti geometry berbeda) | ✗ | ✓ | — | **ya (move A hilang)** | `moved:false, resized:true` |
| label vs label | ✗ | ✓ | — | ya, by design | label B |
| style fillColor vs strokeColor (cell sama, key berbeda) | ✗ | ✓ | — | **ya (fill A hilang)** | `fillKept:false, strokeKept:true` |
| style key sama | ✗ | ✓ | — | ya, by design | nilai B |
| atribut k1 vs k2 (cell sama) | ✗ | ✓ | — | **ya (k1 hilang)** | `k1Kept:false, k2Kept:true` |
| delete X vs edit X | ✓ | ✓ | — | edit dibuang diam-diam | X terhapus |
| edit X vs delete X | ✓ | ✓ | — | edit dibuang | X terhapus |
| group [X,Y] vs move X | ✗ | ✓ | — | tidak hilang, tetapi **niat salah**: geometry absolut B ditafsirkan relatif ke grup | X di grup, geo (40,140) relatif |
| reorder X vs reorder Y | ✓ | ✓ | — | tidak | `X,Z,C,E,Y` |
| reorder X vs reorder X | ✓ | ✓ | — | ya, by design | B menang |
| add page vs add page | ✗ | **✗** | ✓ (adapter diff); ✗ dengan `merge` | tidak | dua page, urutan R |
| reparent X→C vs move X | ✓ | ✓ | — | tidak | X di C |
| reparent X→C vs delete C | ✗ | **✗** | ✓ (adapter diff dan `merge`) | X ikut terhapus di R | X dan C hilang |
| sel berbeda (move X, label Y) | ✓ | ✓ | — | tidak | keduanya |
| add vs add (ID berbeda) | ✗ (urutan) | **✗** | ✓ (adapter diff); ✗ dengan `merge` | tidak | keduanya, urutan R |
| connect X→Z vs delete Z | ✓ | ✓ | — | edge kehilangan target | E2 source X, target null |
| offline A (3 op) vs B (2 op), reconnect | ✓ | ✓ | — | sesuai LWW | X dipindah A, label X dari B, Z terhapus |

Ringkasan (EVIDENCE E5/E5R):
- S1 divergen di 10/18 → relay naif tidak boleh dipakai.
- S3 konvergen 15/18; 3 gagal adalah kasus **struktur/urutan** (urutan sibling/page dan cascade delete) karena patch ditangkap relatif terhadap pandangan penulisnya.
- S3 + repair berbasis checksum + adapter diff: **18/18 konvergen** di harness ini. `merge` XML tidak cukup (gagal di 2 kasus urutan).
- **Lost update** pada properti yang dikemas sebagai blob (`geometry`, `style`, `xmlValue`) terjadi di semua strategi → granularitas per key wajib di semantic op.
- Deterministik: ya untuk urutan server yang sama (semua run S3 = R). Penolakan server: tidak ada di eksperimen ini (tidak ada lapisan validasi).
- Konflik yang terlihat user: tidak ada sama sekali; draw.io tidak memberi tanda → harus disediakan platform (audit + notifikasi "perubahan Anda ditimpa").
- Auditability: setiap patch punya asal dan urutan di harness; di produksi tercatat sebagai op (ADR-0007).

## 8. Benchmark payload & latensi (§15)
Satu operasi move pada `v1`, median 5 sampel (sampel pertama = warm-up, ikut ditampilkan di JSON). EVIDENCE E6.

| Cells | XML penuh | XML deflate | Patch native | Semantic op (kandidat, JSON) | Op → autosave | Apply patch (B) | `merge` XML penuh |
|---|---|---|---|---|---|---|---|
| 50 | 10.164 B | 745 B | 102 B | 220 B | 10,9 ms | 13,1 ms | 18 ms |
| 500 | 102.065 B | 4.184 B | 102 B | 220 B | 33,7 ms | 28,4 ms | 62 ms |
| 5.000 | 1.035.046 B | 37.484 B | 102 B | 220 B | 294,4 ms | 216,6 ms | 588 ms |

- Target awal "500 cell, apply < 100 ms": **PASS** (28,4 ms apply, 33,7 ms capture) di mesin dev; **FAIL** pada 5.000 cell (~217–294 ms) dan **tidak realistis** (lantai graph 117 ms).
- Biaya tumbuh linier terhadap ukuran diagram, **bukan** terhadap ukuran payload. Rincian terukur (E10, SPIKE-002 §14 #11): pada 5.000 cell update graph draw.io murni 117 ms (autosave mati), jalur autosave+diffSync bawaan menambah ±136 ms (clone 31 + hash 34 + diff 19 + XML 18), sisanya pesan/serialisasi (op→event 338 ms). Lantai 117 ms tidak bisa dikurangi tanpa mengubah draw.io.
- Semantic op lebih besar dari patch native karena metadata (`operationId`, `documentId`, sequence) — tetap 0,2 KB.
- Biaya server untuk sequencing + encode + apply LWW (Go, CPU saja, `spikes/spike-002-oplog`): p50 3–4 µs, p99 18–29 µs per op (run sibuk; run ulang tenang 2–5 µs, lihat SPIKE-002 §9); burst 10.000 op = 71 ms (11 ms pada run tenang) CPU; replay 1 juta op ke dokumen 5.000 cell ±165 ms; 200 dokumen paralel ±1,49 juta op/detik; JSON ±270 B/op. EVIDENCE `spike-002-oplog/results.json`.
- Mesin lab (VM ARM kecil) dan browser user biasa akan lebih lambat: **UNKNOWN**, diukur ulang di Phase 5.

## 9. Benchmark operation log (§23)
| Ukuran | Hasil | Label |
|---|---|---|
| CPU sequencing/encode/apply | lihat §8 | EVIDENCE |
| Insert latency PostgreSQL pada 100 / 1.000 / 10.000 op/s | belum diukur (tidak ada server PostgreSQL untuk spike; VM lab di luar scope task ini) | UNKNOWN |
| Pertumbuhan DB | estimasi ±270 B JSON + overhead baris/indeks (±60–100 B) ≈ 0,35 KB/op → 1.000 op/s terus-menerus ≈ 30 GB/hari tanpa kompaksi | INFERENCE |

Metodologi yang akan dijalankan (Phase 5, DB uji sekali pakai, bukan produksi):
1. Tabel `ops(document_id, server_seq, operation_id UNIQUE, actor…, payload jsonb, created_at)` PK `(document_id, server_seq)`; varian `bytea`.
2. Generator 100 / 1.000 / 10.000 op/s selama 60 s, 1 / 50 / 500 dokumen; ukur p50/p95/p99 commit (single insert vs batch 5 ms vs COPY).
3. Replay: muat snapshot + N op (10³–10⁶); kompaksi: tulis snapshot baru tiap K op / T detik, hapus op < snapshot setelah retensi.
4. Reconnect: klien minta op sejak `lastSeq` (index range scan) — ukur untuk gap 10 / 1.000 / 100.000.
5. Ukuran: `pg_total_relation_size` per 1 juta op, WAL per detik.

## 10. Kriteria penerimaan (§33)
| # | Pertanyaan | Status | Ringkasan bukti |
|---|---|---|---|
| 1 | Plugin dapat dimuat tanpa patch image? | **PASS** | Host-injected `Draw.loadPlugin` (E1); `?p=` custom FAIL; settings perlu `ALLOW_CUSTOM_PLUGINS` |
| 2 | Change event dapat ditangkap? | **PASS** | `mxEvent.CHANGE` semua op (E2) dan `autosave`+`diffSync` semua op dokumen (E2); `mxEvent.CHANGE` bukan batas integrasi (NO) |
| 3 | Semantic operation dapat dibentuk? | **CONDITIONAL** | Patch native memuat semua informasi per cell/properti (E2); per-key untuk style/atribut/geometry butuh nilai lama dari shadow adapter — penerjemah belum diprototipe |
| 4 | Operation dapat direplay? | **PASS** | 11 patch (termasuk 2 undo) direplay ke replika lain → state identik (E3) |
| 5 | Remote op diterapkan tanpa echo loop? | **PASS** | 0 echo, undo lokal utuh, seleksi/page aman, duplikat idempoten (E4); edit in-place saat patch masuk UNKNOWN |
| 6 | Native diff/patch dapat digunakan? | **CONDITIONAL** | Ya sebagai mekanisme capture/apply/repair di klien dan target lowering (E2–E5R, E7); tidak sebagai wire publik/persistensi (blob, tanpa schema, semantik hanya di JS) |
| 7 | Bagaimana perilaku konflik? | **PASS (terdokumentasi)** | Matriks §7: S1 10/18 divergen; S3 15/18; S3+repair 18/18; lost update pada blob; niat salah pada group-vs-move |
| 8 | Payload/performa? | **CONDITIONAL** | 500 cell lolos target (28 ms); 5.000 cell 217–294 ms (biaya O(N) draw.io per perubahan); DB UNKNOWN |
| 9 | Model op cukup untuk MCP/AI? | **CONDITIONAL** | Patch buatan server bisa diterapkan (E7); kontrak semantic op (ADR-0010) dan model baca sisi server belum diprototipe |
| 10 | Model kolaborasi yang diusulkan convergent? | **CONDITIONAL** | Konvergen 18/18 dengan S3+repair di harness deterministik 2 klien; belum diuji 3+ klien, jaringan nyata, fuzz, dan model Go |

## 11. Kesimpulan putaran 1
1. Batas integrasi = **protokol embed `diffSync`/`patch`** (terdokumentasi), bukan `mxEvent.CHANGE` dan bukan XML merge.
2. Patch native **bukan** semantic operation: blob `style`/`geometry`/`xmlValue` menyebabkan lost update, dan semantiknya hanya ada di JS.
   Karena itu: patch native = format lokal adapter ↔ draw.io; Semantic Operation = kontrak platform (ADR-0010).
3. Pola yang terbukti konvergen di harness: **server sequencing + klien menerapkan ulang op lokal yang belum di-ack + repair berbasis checksum**.
   Ini rebase sederhana dengan LWW per properti; **tidak memerlukan transformasi OT**.
4. Repair membutuhkan state kanonik di server. Di harness, R adalah draw.io (JS). Untuk produksi, server Go harus memegang model dokumen
   semantik sendiri dan checksum kanonik yang bisa dihitung sama oleh adapter → **belum dibuktikan** (putaran 2).
5. Performa draw.io sendiri menjadi batas: ±200–300 ms per perubahan di 5.000 cell. Ini batas produk (dokumentasikan ukuran dokumen yang didukung), bukan masalah server.

## 12. Putaran 2 (wajib sebelum Phase 5)
- R2-1: Model dokumen Go (map cell, properti per key, urutan fractional, tombstone) + checksum kanonik yang sama di adapter JS; round-trip XML draw.io → model Go → XML tanpa kehilangan.
- R2-2: Adapter patch native → semantic op (per key) dan semantic op → patch native; contract test semua jenis op E2.
- R2-3: Fuzz/property test konvergensi (3–5 klien, urutan acak, drop/duplikat/delay pesan) melawan model Go.
- R2-4: Edit in-place (teks sedang diketik) saat patch remote masuk; undo setelah rebase.
- R2-5: Benchmark PostgreSQL op log (§9) + sequencer multi-replika.
- R2-6: Ukur ulang E6 di browser/mesin kelas user dan di VM lab.

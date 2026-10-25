# ADR-0006 — Model perubahan & kolaborasi

Author / Agent: Claude Code
Timestamp: 2026-10-25 00:55 (revisi 2026-10-25 02:45)
Status: **Accepted untuk lab (2026-10-25)** — kriteria integrasi terpenuhi di E2E nyata; kantor UNVERIFIED. Teks asli: "Proposed (CURRENT HYPOTHESIS …)". Bukti model-level kuat (SPIKE-002 §11) tetapi **integrasi draw.io↔adapter↔model belum terbukti**; Accepted hanya bila kriteria di bawah terpenuhi.

## Context
Kolaborasi beberapa user (dan AI) pada satu diagram draw.io tanpa mengubah draw.io. Bukti dari SPIKE-001 putaran 1:
- draw.io menyediakan capture/apply perubahan granular lewat protokol embed `diffSync`/`patch` (per page/cell/properti; `style`,
  `geometry`, `xmlValue` sebagai blob), terdokumentasi tetapi tanpa schema/janji stabilitas. EVIDENCE.
- Relay naif patch → divergen 10/18 skenario. Server sequencing + re-apply op lokal yang belum di-ack → 15/18; ditambah repair
  berbasis checksum → 18/18 di harness deterministik. Lost update pada blob. EVIDENCE E5/E5R.
- Biaya draw.io per perubahan tumbuh linier dengan ukuran diagram (±217–294 ms di 5.000 cell). EVIDENCE E6.

Prinsip pemisahan (wajib): **wire format ≠ domain operation ≠ merge algorithm ≠ persistence format**.

| Lapisan | Pilihan (hipotesis) |
|---|---|
| Format lokal adapter ↔ draw.io | patch native draw.io (`u/i/r`) — privat di klien |
| Domain operation / wire klien ↔ server | Semantic Operation Contract (ADR-0010), JSON berversi |
| Merge algorithm | server sequencer + LWW per properti-key + aturan struktural deterministik + rebase klien (re-apply) + repair snapshot |
| Persistence | snapshot S3 + op log PostgreSQL (ADR-0007), format internal berversi, bukan API publik |

## Perbandingan 6 model
Skor: ✓ baik · ~ sedang/bersyarat · ✗ buruk. Label bukti di kolom terakhir.

| Kriteria | A. Snapshot + lock | B. XML merge | C. Semantic ops (tanpa aturan merge) | D. OT | E. CRDT (library) | F. Hybrid (usulan) |
|---|---|---|---|---|---|---|
| Correctness | ✓ (satu penulis) | ✗ (urutan tak terdefinisi; gagal 2 kasus urutan di E5R) | ~ (tergantung urutan) | ~ (benar bila matriks transform benar) | ✓ (teruji library) | ~→✓ (terbukti di harness; perlu fuzz) |
| Convergence | ✓ trivial | ✗ | ✗ tanpa sequencer | ✓ dengan server pusat (TP1 cukup) | ✓ | ✓ (S3+repair 18/18) |
| Kompleksitas implementasi | ✓ rendah | ✓ rendah | ~ | ✗ tinggi | ~ (library) / ✗ (buat sendiri) | ~ |
| Kompatibilitas draw.io | ✓ (`load`/`autosave` XML) | ✓ (`merge`) | ✓ (lowering ke patch, E7) | ~ (butuh transform di klien atas patch blob) | ✗ (draw.io bukan dokumen Yjs; butuh binding dua arah model draw.io ↔ Y.Doc) | ✓ (adapter + `patch`) |
| Validasi sisi server | ~ (XML utuh) | ✗ | ✓ | ✓ | ✗ (update biner opak; Go tidak bisa memeriksa tanpa port CRDT) | ✓ |
| Otorisasi per operasi | ✗ (hanya per simpan) | ✗ | ✓ | ✓ | ✗ (tanpa decode semantik) | ✓ |
| Auditability | ~ (diff antar versi) | ✗ | ✓ | ✓ | ~ | ✓ |
| Kompatibilitas MCP | ~ | ✗ | ✓ | ~ (agen harus ikut protokol transform) | ✗ | ✓ |
| Kompatibilitas AI | ~ | ✗ | ✓ | ~ | ✗ | ✓ |
| Offline | ✗ | ~ | ~ | ~ (rebase panjang) | ✓ | ~ (rebase re-apply; terbukti untuk 3 op offline E5) |
| Efisiensi payload | ✗ (O(dokumen)) | ✗ (O(dokumen), 1 MB @5.000) | ✓ (±0,2 KB) | ✓ | ✓ | ✓ |
| Replay | ✓ (versi) | ✗ | ✓ | ✓ | ✓ | ✓ |
| Debuggability | ✓ | ✗ | ✓ | ✗ | ~ | ✓ (op terbaca manusia) |
| Kompleksitas operasional | ✓ | ✓ | ~ | ~ | ~ | ~ |
| Kompleksitas implementasi Go | ✓ | ✓ | ✓ | ✗ | ✗ (Yjs = JS, yrs = Rust/cgo, Automerge = Rust/JS) | ✓ (map + LWW + fractional index) |
| Ekstensibilitas | ~ | ✗ | ✓ | ✗ (tiap jenis op baru menambah baris+kolom matriks) | ✓ | ✓ (jenis op baru = aturan merge per properti) |
| Bukti | EVIDENCE (E6 XML) | EVIDENCE (E5R merge gagal) | INFERENCE | INFERENCE (analisis §OT) | INFERENCE | EVIDENCE harness + INFERENCE |

## Analisis OT (pasangan operasi nyata)
OT dengan server pusat cukup memenuhi TP1 (transform(a,b) lalu b ≡ transform(b,a) lalu a terhadap state server); TP2 hanya diperlukan
tanpa server pusat. Yang dinilai: apakah fungsi transform bisa dijaga benar untuk struktur draw.io.

| Pasangan | Transform yang diperlukan | Masalah |
|---|---|---|
| move + move (cell sama) | pilih satu (LWW) atau komposisi delta | bila op = posisi absolut, transform = LWW (bukan OT sebenarnya); bila delta, komposisi benar tetapi draw.io hanya mengeluarkan absolut (E2) |
| move + delete | move menjadi no-op | mudah |
| move + reparent | geometry relatif terhadap parent; move yang dihitung di parent lama harus dikonversi ke koordinat parent baru | butuh pengetahuan geometri (offset grup/container, rotasi, edge relatif); E5 group-vs-move menunjukkan hasil "niat salah" tanpa konversi |
| reorder + reorder | transform indeks posisi | indeks bergeser saat insert/delete/reparent bersamaan; kombinasi dengan reparent membuat matriks bercabang |
| reparent + delete | reparent ke parent yang dihapus → anak ikut terhapus atau dipindah ke root | kebijakan, bukan transform; E5 menunjukkan dua replika berbeda tanpa aturan tunggal |
| reparent + reparent (siklus) | A: X ke dalam Y, B: Y ke dalam X | OT klasik tidak mendeteksi siklus; perlu validasi global (masalah tree-move yang terkenal) |
| setValue + setValue | LWW | trivial |
| add + add | ID unik → keduanya tetap; urutan sibling via indeks → transform indeks | E5 add-add divergen saat urutan direkonstruksi relatif per penulis |
| pageAdd + pageAdd | sama dengan add+add pada urutan page | E5 addpage-addpage divergen |

Failure mode: (1) **TP1 bergantung pada kebenaran tiap pasangan**; jumlah pasangan tumbuh kuadratik terhadap jenis op (13 jenis kandidat → 169 pasangan,
belum termasuk properti per key); (2) **tree operations** (reparent/group) memerlukan validasi siklus dan konversi geometri yang bukan transform lokal;
(3) **z-order dengan indeks** rapuh terhadap insert/delete/reparent bersamaan; (4) **page operations** mengulang masalah urutan.
Bukti empiris: semua kasus yang gagal di S3 (E5) adalah kasus struktur/urutan — tepat area tersulit OT. Sebaliknya, kasus properti
cukup dengan LWW tanpa transform. **Kesimpulan: OT tidak dipilih**; manfaat utamanya (server otoritatif, urutan tunggal) sudah didapat
dari sequencer, sementara biayanya (matriks transform) ada di area yang justru bisa diselesaikan dengan representasi yang lebih baik
(fractional index, validasi move di server). Label: EVIDENCE (E5) + INFERENCE (analisis).

## Analisis CRDT (konsep dipisahkan dari library)
| Bagian dokumen | Konsep CRDT yang cocok | Catatan |
|---|---|---|
| Kumpulan cell per page | map kunci `(pageId, cellId)` + add-wins/remove-wins set | ID cell dipilih klien; server menolak ID bentrok yang bukan miliknya |
| Properti cell (`style.<key>`, `geometry.x/y/width/height/points`, `value`, `attr.<name>`, `source`, `target`, `parent`) | LWW-register per key | memecah blob → menghilangkan lost update E5 |
| Urutan sibling & page | sequence via **fractional index** (kunci urut string) | reorder bersamaan tidak bentrok; tie-break `(index, actorId, opId)` |
| Hierarki (group/container) | move-tree (Kleppmann dkk.) | dengan sequencer pusat cukup: server menolak move yang membuat siklus; LWW pada `parent` |
| Delete | tombstone | edit setelah delete dibuang + dicatat sebagai konflik terlihat user |
| Label teks | LWW utuh (awal) | sequence CRDT karakter hanya bila data menunjukkan perlu |

- **Tombstone & metadata**: per properti cukup `(serverSeq)` karena urutan total dari server; tidak perlu vector clock per elemen.
  Tombstone dibuang saat kompaksi snapshot setelah semua klien melewati seq tersebut (klien yang lebih tua dipaksa resync).
- **Offline**: klien menyimpan op lokal + `baseSeq`; saat kembali, server menerapkan dalam urutan kedatangan (LWW), klien rebase
  dengan re-apply (terbukti untuk 3 op offline, E5). Offline panjang dengan banyak konflik → resync snapshot.
- **Otorisasi & inspeksi semantik**: karena op tetap semantik dan didekode server, otorisasi per op dan audit tetap mungkin
  (hal yang hilang bila memakai update biner library).
- **Library (Yjs/Automerge)**: tidak dipilih sebagai sumber kebenaran — Go tidak bisa memeriksa update tanpa port (yrs via cgo /
  automerge-rs), binding draw.io ↔ Y.Doc dua arah harus dibangun dan dirawat, dan otorisasi per op menjadi sulit. Tetap alternatif
  bila putaran 2 menunjukkan adapter semantic op tidak layak.

## Decision (hipotesis, status Proposed)
**F. Hybrid**: Semantic Operation Contract (ADR-0010) + **server sequencing** per dokumen + **semantik konvergensi gaya CRDT**
(LWW per properti-key dengan urutan server, add-wins insert, tombstone delete, fractional index untuk urutan, move tervalidasi
server untuk hierarki) + **rebase klien dengan re-apply op lokal yang belum di-ack** + **repair snapshot berbasis checksum kanonik** +
**snapshot berkala** + **op log append-only**.

```mermaid
sequenceDiagram
  participant D as draw.io (iframe)
  participant A as adapter (host page)
  participant S as document-collab (Go)
  D->>A: autosave {patch, checksum}  (LOCAL)
  A->>A: patch native -> semantic ops (per key, pakai shadow)
  A->>S: ops {operationId, baseServerSeq}
  S->>S: authn token internal, authz per op, validasi, sequence, LWW apply, append oplog (commit)
  S-->>A: ack {serverSeq, checksum}
  S-->>A: broadcast ops lain {serverSeq, checksum}  (REMOTE)
  A->>A: semantic op -> patch native; re-apply op lokal belum di-ack
  A->>D: action patch
  D-->>A: patch ack {checksum}
  A->>S: bila checksum kanonik beda: minta snapshot -> diffPages(own, snapshot) -> patch (repair)
```

Mode fallback yang selalu tersedia: **A. Snapshot + lock** (satu editor per dokumen), dipakai di Phase 3–4 sebelum Phase 5
dan saat collab tidak tersedia.

## Consequences
- Phase 5 tidak boleh dimulai sebelum SPIKE-003 (Phase 4b: integrasi adapter + model Go + fuzz integrasi) lulus dan ADR-0010 dibekukan (gate MASTER-PLAN §24).
- Checksum draw.io (`getHashValueForPages`) tidak bisa dihitung server Go → butuh **checksum kanonik milik platform** yang dihitung
  adapter dari model draw.io dan server dari model Go (UNKNOWN, R2-1).
- Konflik harus dibuat terlihat (op yang ditimpa/dibuang dicatat dan dapat dinotifikasi), karena draw.io tidak menampilkannya.
- Ukuran dokumen yang didukung untuk realtime dibatasi oleh performa draw.io (±500–1.000 cell nyaman; 5.000 cell terasa berat). Batas pasti dari R2-6.

## Alternatives
- **A. Snapshot + lock** — diterima sebagai fallback/mode awal, ditolak sebagai satu-satunya model (tanpa edit bersamaan).
- **B. XML merge** — ditolak sebagai final (payload O(dokumen), urutan tak terdefinisi, tanpa otorisasi per op). EVIDENCE E5R/E6.
- **C. Semantic ops tanpa sequencer** — ditolak (divergen, setara S1).
- **D. OT** — ditolak (analisis di atas).
- **E. CRDT library sebagai sumber kebenaran** — ditunda; alternatif bila putaran 2 gagal.

## Status kriteria penerimaan (per 2026-10-25)
| # | Kriteria | Status | Bukti |
|---|---|---|---|
| 1 | R2-1 round-trip XML ↔ model Go tanpa kehilangan (≥ 20 diagram nyata) | **PARTIAL** | representasi kanonik JS↔Go 174/174 (24 tulisan tangan + 150 acak); korpus diagram *nyata* + model Go sebagai sumber kebenaran belum |
| 2 | R2-2 adapter patch native ↔ semantic op dua arah, contract test | **UNVERIFIED** | hanya lowering patch buatan tangan (E7); penerjemah belum ada |
| 3 | R2-3 fuzz 3–5 klien, ≥ 10.000 urutan acak, 100% konvergen, 0 lost update beda key | **PASS di model Go** (12.000 skrip, 1,15 juta op, 0 kegagalan; 6 mutasi tertangkap); **UNVERIFIED di integrasi** | spike-004 |
| 4 | R2-4 edit in-place dan undo setelah rebase aman | **PARTIAL** | E8/E9: tidak crash, tetapi menimpa edit remote secara diam-diam → perlu event `overridden` + undo per-actor |
| 5 | p95 A→B < 250 ms @500 cell di lab | **UNVERIFIED** | komponen terukur (capture 33 + apply 28 ms + PG commit ~4–8 ms); end-to-end jaringan di lab belum |
| 6 | Benchmark PostgreSQL op log | **PASS (dev)** | ADR-0007; ulang di VM lab/office |

Perubahan hipotesis akibat bukti: (a) op disimpan dalam bentuk **resolved** (idempotensi); (b) geometri sebagai **intent absolut** dengan reparent mempertahankan posisi absolut — tanpa itu 1.661 pelanggaran wrong-intent per 3.000 skrip; (c) overwrite konkuren (0,38% op diterapkan) harus dimunculkan ke penulis; (d) fractional index butuh `rebalanceSiblings`.

## Kriteria penerimaan (§7) untuk mengubah status menjadi Accepted
1. R2-1: round-trip XML ↔ model Go tanpa kehilangan untuk korpus diagram uji (≥ 20 diagram nyata, termasuk grup, container, edge berwaypoint, page banyak).
2. R2-2: adapter mengubah semua jenis op E2 dua arah; contract test lulus.
3. R2-3: fuzz 3–5 klien, ≥ 10.000 urutan acak (delay/duplikat/drop+retry): 100 % konvergen setelah repair, 0 lost update untuk properti beda key.
4. R2-4: edit in-place dan undo setelah rebase tidak merusak state.
5. Latensi end-to-end op (klien A → klien B) p95 < 250 ms pada diagram 500 cell di lab.
Bila gagal: catat **Accepted alternative** — snapshot + lock (minimal), atau CRDT library (Yjs/Automerge) dengan konsekuensi otorisasi/MCP, atau model lain yang terbukti.

## Evidence
`docs/spikes/SPIKE-001-drawio-change-model.md` §4–§10; `spikes/spike-001/results/*.json`.

## Addendum 2026-10-25 — bukti integrasi draw.io ↔ adapter ↔ model (SPIKE-003 secara de facto)
- Browser menyimpan tiga keadaan: **confirmed C** (op ter-resolve berurutan dari server), **pending** (batch terkirim belum diakui) dan **E** (isi editor yang diketahui). Perubahan lokal = `diff(E, L)` (bukan terhadap T — perubahan remote yang belum tampil tidak boleh terbaca sebagai penghapusan lokal; bug ini ditemukan E2E dan diperbaiki). Perubahan remote/ack → `toPatch(L, T)` ke aksi `patch` embed. Setelah penerapan `E := T`.
- Nack/ditolak ⇒ editor dikembalikan ke T (perubahan hilang terlihat oleh pengguna dengan toast), tanpa loop kirim ulang.
- Hash CANON-v1 dari server diverifikasi terhadap C; selisih ⇒ resync penuh. Reconnect memakai `since=lastSeq` (catch-up) atau join penuh.
- Bukti: 44 cek E2E (3 pengguna paralel di Chrome), 1799 kasus diff JS→Go identik, kriteria 1–4 terpenuhi; kriteria 5 (latensi p95) lihat README "Performa".

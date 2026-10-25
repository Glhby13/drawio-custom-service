# ADR-0010 — Semantic Operation Contract

Author / Agent: Claude Code
Timestamp: 2026-10-25 01:05 (revisi 2026-10-25 02:30)
Status: **Proposed** (kontrak kandidat; direvisi oleh bukti fuzz SPIKE-002 — lihat "Revisi berdasarkan bukti")

## Context
Satu model perubahan harus dipakai bersama oleh adapter draw.io, kolaborasi, otorisasi, validasi, op log, audit, MCP, dan AI.
Bila tiap jalur punya model command sendiri, otorisasi dan audit akan menyimpang. Patch native draw.io tidak cocok sebagai kontrak
platform: blob `style`/`geometry`/`xmlValue` → lost update (EVIDENCE SPIKE-001 §7), tanpa schema resmi, semantik hanya di JS.
Namun patch native terbukti lengkap sebagai sumber informasi dan sebagai target lowering (EVIDENCE E2, E7).

```
draw.io ──patch native──> adapter ──Semantic Operation──> authz · validasi · sequencer · merge · op log · audit
                                                       ^
MCP tools / AI ─────────────────────────────────────────┘   (memakai kontrak yang sama, tanpa jalur lain)
```

## Decision
### Envelope (kandidat)
```json
{
  "schemaVersion": 1,
  "operationId": "018f6c1e-6a1b-7c3d-9e4f-0a1b2c3d4e5f",
  "documentId": "d-123",
  "actor": { "id": "u-42", "type": "human", "onBehalfOf": null, "agent": null, "model": null, "sessionId": "s-9" },
  "clientSequence": 17,
  "baseServerSequence": 1203,
  "serverSequence": null,
  "hlc": null,
  "batchId": "b-77",
  "type": "setStyle",
  "payload": { "pageId": "p1", "cellId": "X", "key": "fillColor", "value": "#ff0000" }
}
```
| Field | Diisi oleh | Aturan |
|---|---|---|
| `schemaVersion` | pengirim | integer; server menolak versi tidak dikenal (`unsupported_schema`) |
| `operationId` | pengirim | UUIDv7; **kunci idempotensi** (unik per dokumen); kirim ulang = ack yang sama, tidak diterapkan dua kali |
| `actor.*` | **server** dari token internal (ADR-0004); nilai dari klien diabaikan | `type`: `human|ai|service`; AI wajib `onBehalfOf`/`agent`/`model` sesuai ADR-0009 |
| `clientSequence` | pengirim | monoton per sesi; mendeteksi celah/duplikat |
| `baseServerSequence` | pengirim | seq terakhir yang sudah diterapkan klien; untuk rebase dan deteksi stale |
| `serverSequence`, `hlc` | server | urutan total per dokumen; HLC untuk audit lintas dokumen |
| `batchId` | pengirim | op satu gestur (mis. group = insert grup + reparent + geometry) diterapkan **atomik** |

### Jenis operasi kandidat (bukan final)
| Type | Payload inti | Aturan merge (ADR-0006) |
|---|---|---|
| `addCell` | `pageId, cellId, kind(vertex|edge), parentId, orderKey, value|attrs, style{}, geometry{}, source?, target?` | add-wins; ID harus unik (bentrok → tolak) |
| `deleteCell` | `pageId, cellId, cascade:true` | tombstone; cascade dievaluasi **di server** pada saat sequence (anak ikut) |
| `setValue` | `pageId, cellId, value` | LWW |
| `setStyle` | `pageId, cellId, key, value|null` | LWW **per key** |
| `setGeometry` | `pageId, cellId, fields{x?,y?,width?,height?,points?,relative?,offset?}` | LWW per field; dalam koordinat parent yang dinyatakan (`parentId` diperiksa) |
| `setAttribute` | `pageId, cellId, name, value|null` | LWW per atribut |
| `connect` / `disconnect` | `pageId, edgeId, end(source|target), terminalId|null` | LWW per ujung; terminal hilang → `null` |
| `reparent` | `pageId, cellId, newParentId, orderKey, geometry?` | LWW pada parent; server menolak siklus; geometry dikonversi oleh pengirim |
| `reorder` | `pageId, cellId, orderKey` | fractional index; tie-break `(orderKey, actorId, operationId)` |
| `addPage` / `deletePage` / `renamePage` / `reorderPage` | `pageId, name?, orderKey?` | add-wins; LWW nama; fractional index urutan |
| `setPageProperty` | `pageId, key, value` (background, grid, pageFormat …) | LWW per key |

`orderKey` (fractional index) menggantikan `previous` draw.io di kontrak; adapter menerjemahkan dua arah.

### Aturan kontrak
- **Idempotensi**: `operationId` unik; server menyimpan hasil ack; duplikat → ack ulang tanpa efek. EVIDENCE pendukung: patch draw.io sendiri idempoten untuk nilai absolut (E4).
- **Ordering**: total per dokumen oleh server (`serverSequence`); klien menerapkan op remote berurutan; celah → minta op sejak `lastSeq` atau snapshot.
- **Validasi**: schema (tipe, ukuran: string ≤ 64 KB, style key whitelist karakter, geometry angka hingga), referensi (page/cell/parent ada dan tidak terhapus),
  invarian (tanpa siklus, edge terminal di page yang sama), batas batch (≤ N op), penolakan = `rejected{code, reason}` ke pengirim saja.
- **Otorisasi**: per op lewat PDP domain (`apply-operation(type)` + kebijakan AI); tidak ada op yang melewati PDP.
- **Replay**: state = snapshot(seq s) + op (s, n]; deterministik karena aturan merge murni fungsi dari urutan.
- **Audit**: setiap op tersimpan dengan `actor`, waktu server, hasil (applied/rejected/overridden), dan op yang menimpanya bila LWW.
- **Kompatibilitas & jenis tidak dikenal**: penambahan field opsional = kompatibel; jenis baru → `schemaVersion` tetap, tetapi server
  dan klien lama **menolak** jenis tidak dikenal (bukan mengabaikan diam-diam); klien lama dipaksa reload editor (versi adapter dicek saat join).
- **Schema version / backward compatibility**: perubahan yang tidak kompatibel menaikkan `schemaVersion`; server mendukung N dan N-1 selama satu rilis; op log menyimpan versi per op dan dimigrasi saat kompaksi.
- **AI/MCP**: tool MCP hanya membangun op kontrak ini (ADR-0008); op AI memakai envelope yang sama dengan `actor.type=ai`.
- **Undo/revert**: undo lokal draw.io menghasilkan patch → op baru biasa (terbukti undo menghasilkan patch, E3). Revert lintas user
  = server membangun **op invers** dari nilai sebelumnya yang tercatat di op log (nilai lama disimpan per op LWW) dan menerapkannya sebagai op baru
  dengan `actor` = yang meminta revert. Tidak ada "hapus dari log".
- **Bukan persistensi**: format penyimpanan op log internal (ADR-0007) boleh berbeda; kontrak ini adalah API.

## Revisi berdasarkan bukti (SPIKE-002, 2026-10-25)
Model-level fuzz (`spikes/spike-004-fuzz`, Go, 12.000 skrip, 1,15 juta op) mengubah kontrak sebagai berikut. Semua perubahan bersifat netral-arsitektur.

1. **Dua bentuk operasi: authored dan resolved.** Op yang ditulis klien/MCP/AI membawa *intent*. **Sequencer menghitung bentuk resolved sekali** (koordinat relatif terhadap parent,
   daftar cascade delete, terminal edge yang dibersihkan) dan **log menyimpan bentuk resolved**; replay, duplikat, dan snapshot-resume tidak pernah menghitung ulang.
   *Bukti:* tanpa ini, properti idempotensi gagal di 28/300 skrip (shrink menjadi 7–9 event): `setPos` absolut→relatif dihitung terhadap posisi parent *saat diterapkan*,
   sehingga menerapkan ulang op lama pada state yang lebih baru memberi hasil lain. Dengan op resolved: 0 kegagalan di 12.000 skrip. Konsekuensi: log tahan terhadap perubahan versi kode konversi.
   ```json
   { "...envelope...": "...", "type": "setPosition", "payload": { "pageId":"p1","cellId":"X","absolute":{"x":484,"y":354} },
     "resolved": { "parentId":"C","relative":{"x":16,"y":308} } }
   { "type": "deleteCell", "payload": {"pageId":"p1","cellId":"C"}, "resolved": { "tombstoned":["C","c1","c2"], "terminalsCleared":["E:target"] } }
   ```
2. **Geometri dinyatakan sebagai intent absolut.** `setPosition` membawa target **absolut**; `reparent` secara default **mempertahankan posisi absolut** (resolved ke relatif oleh sequencer).
   Mode relatif hanya untuk geometri yang memang relatif (label edge `relative=1`, `offset`). *Bukti:* geometri relatif naif menghasilkan 1.661 pelanggaran *wrong intent* dalam
   3.000 skrip (292 ribu op) — sel berakhir di posisi lain dari yang dimaksud penulis setelah reparent/move bersamaan; mode intent: 0 dalam 12.000 skrip (oracle independen berbasis koordinat absolut).
   Ini selaras dengan contoh `group vs move` di SPIKE-001 §7 ("niat salah").
3. **Setiap op berakhir tepat satu dari: applied (di log) atau nacked (dengan alasan) — tidak pernah hilang diam-diam.** Alasan nack yang teramati: `cell_gone`, `page_gone`,
   `parent_gone`, `terminal_gone`, `cycle`, `duplicate_*`, `bad_order_key`, `forbidden`. Pengirim *selalu* menerima nack (property accounting). Mutasi "server membuang op tanpa ack/nack" tertangkap di 600/600 skrip.
4. **Overwrite konkuren adalah kejadian yang harus dilihat user.** LWW per key menimpa tulisan aktor lain yang konkuren (`Base < seq tulisan sebelumnya`): 3.024 kejadian per 1,15 juta op
   (±0,38% dari op yang diterapkan). Kontrak menambah event server→klien `overridden{operationId, byOperationId, byActor}` ke penulis yang tertimpa; UI/MCP/AI memutuskan cara menampilkannya.
5. **Kunci urutan (fractional index) tumbuh.** Penyisipan berulang di titik yang sama memperpanjang kunci ±1 karakter per 5–6 sisipan (2.000 sisipan di depan → 334 karakter). Kontrak menambah op
   `rebalanceSiblings{parentId}` yang dihasilkan sequencer secara atomik (resolved: pemetaan id→kunci baru) bila panjang kunci > 64.
6. **Identitas cell di-namespace per page.** draw.io hanya menjamin ID unik per page; model fuzz memakai ID unik per dokumen. Kontrak tetap memakai pasangan `(pageId, cellId)` sebagai kunci (sudah demikian di payload di atas); pemetaan di model Go harus per page.
7. **Op yang ditolak bukan bagian log.** Sequence hanya dialokasikan untuk op yang diterima; nack tidak mengonsumsi `serverSequence`. Duplikat dikenali lewat `operationId` (EVIDENCE: 223.729 duplikat diabaikan tanpa efek).
8. **`previous_values` untuk revert** disimpan oleh sequencer di sisi log (bukan di payload klien) agar revert lintas user tidak bergantung klien.

## Consequences
- Adapter klien harus menyimpan shadow untuk menghitung diff per key dari blob draw.io (R2-2).
- Server Go memegang model dokumen semantik untuk validasi, merge, checksum kanonik, dan read model MCP/AI (R2-1).
- Jumlah op per gestur bisa lebih banyak dari patch native (group = 1 patch → beberapa op), sehingga batch atomik diperlukan.

## Alternatives
- Patch native draw.io sebagai kontrak — ditolak (blob → lost update; tanpa schema; semantik JS saja). EVIDENCE.
- JSON Patch (RFC 6902) atas XML/JSON dokumen — ditolak (path berbasis indeks, rapuh terhadap urutan; tidak bermakna untuk otorisasi).
- Command per jalur (MCP sendiri, collab sendiri) — ditolak (prinsip satu kontrak).

## Evidence
SPIKE-001 §2, §4, §7, §8 (ukuran op kandidat 220 B), E7 (patch buatan server bisa diterapkan).

# ADR-0017 — Tata letak dan pemeriksaan di sisi server untuk agen AI (build_diagram, lint_diagram, layout hierarkis)

Author / Agent: Claude Code
Timestamp: 2026-10-31 03:30
Status: **Accepted untuk lab (2026-10-31)** — menambah ADR-0006/0010 (kontrak operasi semantik tidak berubah) dan ADR-0016 (token per diagram).

## Context
Diagram yang dihasilkan agen lewat MCP berantakan: garis menembus shape dan label, container saling menimpa, teks meluap, puluhan persilangan.
Akar masalahnya bukan satu model tertentu: sebuah model menulis koordinat `x,y` dan XML **tanpa melihat hasilnya**, dan MCP lama tidak membantu
(`layout` hanya `layered`/`grid` untuk shape tingkat atas, tanpa container dan tanpa routing edge; tidak ada cara memeriksa hasil; `connect`
mengizinkan edge ke sel yang tidak ada; deskripsi tool tidak membimbing gaya).

## Decision
Agen **mendeskripsikan** graf, server **menghitung** geometri dan **memeriksa** hasilnya. Semua di paket `internal/model` (tanpa I/O):
- **`build_diagram`** (`POST /documents/{id}/build`): spesifikasi deklaratif (groups, nodes, edges; `kind`, `color`, `direction` TB/LR). Ukuran shape dari
  panjang label, container dipasang pas di sekeliling anggotanya, semua dimasukkan sebagai **satu batch atomik** (semua atau tidak sama sekali, satu langkah
  yang bisa di-revert). Validasi ketat dan menyeluruh: id baru dan unik, `kind`/`color` dari daftar (tidak ada default diam-diam), batas 480 sel per panggilan,
  label di-escape (tampil literal, tidak pernah HTML). Respons memuat laporan lint.
- **`lint_diagram`** (`GET /documents/{id}/lint`, hanya-baca): `overlap`, `outside_container`, `label_overflow`, `edge_through_shape`, `edge_label_on_shape`,
  `dangling_edge`, `edge_crossings`, tiap temuan dengan sel dan saran perbaikan. Rute edge direkonstruksi dari port dan waypoint seperti gaya orthogonal draw.io.
- **`layout` algo `hierarchical` (default) / `hierarchical-lr`**: mesin yang sama untuk diagram yang sudah ada; hanya nilai yang berubah menjadi operasi
  (idempoten: jalan kedua = 0 operasi, tidak menghabiskan kuota AI).
- **Instruksi MCP** (dikirim saat `initialize`) memuat panduan gaya: pakai `build_diagram`, jangan memilih koordinat, `lint_diagram` lalu `layout`, satu topik per
  halaman (≤ ~25 shape), label pendek, `kind`/`color` konsisten.
- `connect` menolak sel yang tidak ada; `add_shape` punya gaya default yang terbaca, label di-escape, angka dibatasi.

### Mesin tata letak (`autolayout.go`)
Sugiyama berlapis: pemecah siklus, layer jalur-terpanjang, urutan barycenter, koordinat dengan rata-rata dua pemadatan. **Strategi "flat"** untuk diagram ber-container:
setiap container mendapat **pita layer** sendiri (container bertumpuk sepanjang alur, tidak saling menyilang), seluruh shape melewati satu layering sehingga edge antar
container berjalan di **jalur kosong** (node dummy) alih-alih menembus shape; edge panjang diberi titik sudut lengkap (vertikal di kolom dummy, belok hanya di jalur antar-layer)
dan **lajur** sendiri yang diurutkan untuk meminimalkan tabrakan; port keluar/masuk disebar di sisi shape. Bila kotak container bertabrakan atau ada edge ke container,
mesin jatuh ke strategi **nested** (container demi container). Deterministik: input sama, hasil sama.

## Consequences
- Biaya operasi: 1 op per shape/edge/container (kuota AI per menit pemilik berlaku; pesan 429 menyarankan menaikkan batas atau membangun bertahap).
- Pengukuran teks adalah **estimasi** (tanpa metrik font); sengaja sedikit pesimistis. Rute edge di editor bisa sedikit berbeda dari rekonstruksi lint.
- Persilangan edge tidak nol pada graf padat (arsitektur 17 edge: 7–13 dari 20+ sebelumnya, nol edge menembus shape); `lint_diagram` melaporkannya sebagai catatan.
- Batas kerja kuadratik: lint melewati cek berpasangan di atas 1.200 shape/edge (dengan catatan `lint_partial`), layout hierarkis menolak halaman > 1.500 sel.
- Temuan sampingan: `FillOrders` (kunci urut untuk penulis API) kini melangkah satu digit (`appendKey`) alih-alih membelah menuju akhir (`Between`); sebelumnya
  halaman dengan > ~1.300 saudara menolak sel baru (`bad_order_key`). `Between` sendiri **tidak diubah** (kontrak cermin dengan `web/model.js`).
- Kontrak operasi semantik (ADR-0010) tidak berubah: semuanya hanya `addCell`/`setPos`/`setProp`.

## Verifikasi
Tes unit `internal/model/build_test.go` (bersih: nol overlap/outside/overflow/edge-menembus-shape pada arsitektur 17 edge TB dan LR; determinisme; validasi; escape; layout
memperbaiki dan stabil; batas ukuran), `internal/mcpsrv/tuning_test.go` (katalog skema valid, instruksi, `connect`, `add_shape`), E2E `e2e/phase11_build_lint.mjs` terhadap lab.

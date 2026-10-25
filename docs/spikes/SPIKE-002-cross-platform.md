# SPIKE-002 — Validasi lintas-platform (ARM64 lab ↔ AMD64 office), re-verifikasi SPIKE-001, fuzz, checksum, PostgreSQL

Author / Agent: Claude Code
Timestamp: 2026-10-25 02:50
Status: **Selesai** untuk lingkup yang bisa dibuktikan di Mac (arm64 native + amd64 emulasi). Item yang butuh RHEL/RKE AMD64 sungguhan ditandai UNVERIFIED (§13).

Label bukti: **EVIDENCE** (dijalankan/dibaca dengan hasil tercatat) · **INFERENCE** (penalaran) · **UNVERIFIED/UNKNOWN** (belum dibuktikan).
Semua prototipe berlabel *DISPOSABLE SPIKE — NOT PRODUCTION CODE* dan berada di `spikes/`.

## 1. Objective
1. Menjawab: apakah arsitektur portable antara lab (MacBook M4, ARM64, Docker Desktop; cluster lab RHEL 9.8 aarch64 + RKE2) dan office (RHEL 9.6/9.8, AMD64, RKE, kemungkinan air-gapped)?
2. Memverifikasi ulang klaim penting SPIKE-001 (jangan menerima hanya karena tertulis).
3. Menutup gap yang tersisa dari review sebelumnya: checksum kanonik JS↔Go, fuzz/property test, benchmark PostgreSQL, edit teks in-place, bottleneck performa.
4. Menghasilkan rekomendasi GO / GO WITH CONDITIONS / NO-GO untuk Phase 1 (lihat MASTER-PLAN, bagian akhir).

## 2. Environment
| Item | Nilai |
|---|---|
| Host | MacBook (Apple Silicon M4, `uname -m` = arm64, 14 CPU), Docker Desktop 29.4.3, image store containerd, buildx v0.29 (platform: linux/amd64, linux/arm64, +ppc64le/s390x/…) |
| VM Docker | 12,5 GB RAM; emulasi amd64 oleh Docker Desktop (Rosetta/QEMU — mekanisme tepatnya tidak dipastikan) |
| Toolchain | Go 1.27.1 (host), `golang:1.27.2-alpine` (build container), Node 26.5 (host) / 24.20 (container) |
| Browser | Chrome 155/156 headless macOS arm64 (host); Chromium 153.0 headless **Linux x86_64** di container `mcr.microsoft.com/playwright:v1.63.0-noble` (amd64) |
| draw.io | `jgraph/drawio:32.6.1`; index digest `sha256:3d0a5a5f…412e1`; amd64 `sha256:71d6ada4…`; arm64 `sha256:693feb89…`; plus `draw.war` rilis v32.6.1 (sha256 `44dd5695…5e42aa`) |
| PostgreSQL | `postgres:18` → 18.6 (Debian), arm64 native, `synchronous_commit=on`, `wal_sync_method=fdatasync`, `shared_buffers=128MB` |
| Beban Mac | 8 VM lab (UTM) berjalan bersamaan; load average 19–31 saat sebagian run → semua angka waktu = *kondisi ukur dev*, bukan server |
| UTM | `utmctl list`: bastion, nginx, kube-controlplane, kube-worker-1, SeaweedFS, PostgreSQL, redis, kafka — **semua aarch64**; tidak ada VM AMD64 |
| Lab cluster | `kube-worker-1`/`kube-controlplane`: RHEL 9.8 (Plow), **aarch64**, RKE2 v1.33.13+rke2r1, SELinux **Permissive** (EVIDENCE via ssh, 2026-10-25). `kubectl` ke API timeout saat Mac sedang berbeban → runtime/CNI tidak dibaca |

Capture mentah: `spikes/spike-006-crossbuild/environment.txt`.

## 3. ARM64 results (Mac + Docker Desktop, native)
| Uji | Hasil | Status |
|---|---|---|
| draw.io 32.6.1 arm64 jalan, Tomcat 9 / OpenJDK 11.0.32.1 / glibc 2.43 / Ubuntu 26.04, `GET /editor/` 200 (subpath via `DRAWIO_SERVER_URL`) | ya | PASS |
| Suite SPIKE-001 (E0–E9) terhadap **image sungguhan** lewat proxy gateway-stand-in | seluruh hasil fungsional identik dengan run `draw.war` statis di SPIKE-001, kecuali port di E0 (artefak) | PASS |
| Hardening container (UID acak, cap-drop, no-new-privileges, read-only) | §7 | PASS dengan syarat GID 0 |
| Build Go multi-service `CGO_ENABLED=0` linux/arm64, ELF aarch64 statis | ya | PASS |
| Fuzz 12.000 skrip INTENT (1.153.381 op) | 0 kegagalan properti, 0 non-determinisme | PASS |
| Checksum kanonik JS↔Go, 174 dokumen | 174/174 identik | PASS |
| PostgreSQL op log benchmark | §12 | PASS |

## 4. AMD64 results (emulasi + Chromium x86-64 native-in-emulation)
| Uji | Hasil | Status |
|---|---|---|
| draw.io 32.6.1 **amd64** jalan di Docker Desktop (emulasi), `GET /editor/` 200 | ya | PASS |
| Webapp di image amd64 vs arm64 (3.122 file, tanpa Pre/PostConfig) | **identik byte-per-byte** (hash agregat `34094c82…c99c40f` sama, termasuk `PreConfig.js` hasil entrypoint) | PASS |
| Suite E0–E9 terhadap image amd64 (browser Mac) | `compare.py`: fungsional **identik** dengan arm64 (E0–E7 + E8/E9) | PASS |
| Suite E0–E5R, E7–E9 di **Chromium 153 Linux x86_64** (container amd64) vs Chrome Mac | fungsional **identik** (`results/cross-compare-browser.json`) | PASS |
| Hardening amd64 (UID 1234 GID 0; cap-drop ALL + no-new-privileges) | start, 200 di `/editor/`, konfigurasi `DRAWIO_*` terpasang | PASS |
| Build Go multi-platform; ELF x86-64 statis, `CGO_ENABLED=0`; dijalankan di container amd64 | fuzz seed sama → **hasil identik** dengan arm64 (400 skrip/38.280 op intent; 200 skrip naive/93 pelanggaran intent sama persis); selftest fractional index identik | PASS |
| Klien PostgreSQL (`pgx`) amd64 melawan PostgreSQL | berjalan, 0 error (angka waktu tidak representatif: emulasi + jaringan Docker) | PASS fungsional / waktu tidak sah |
| Kinerja pada x86 sungguhan | tidak diukur | UNVERIFIED |

Catatan: "amd64" di sini = *kebenaran fungsional* di bawah emulasi/Chromium x86-64, **bukan** bukti kinerja atau perilaku RHEL/RKE.

## 5. draw.io image results
| Pertanyaan | Jawaban | Bukti |
|---|---|---|
| Multi-arch? | ya: index + manifest linux/amd64 dan linux/arm64 + 2 attestation (provenance SLSA) | `imagetools inspect` |
| Base | Ubuntu 26.04, Tomcat 9, OpenJDK 11.0.32.1, glibc 2.43 (sama di kedua arch) | `docker exec` |
| Konten editor berbeda antar arch? | **tidak**: webapp byte-identik; hanya JVM/OS yang native | hash tree |
| Beda dari `draw.war` rilis? | hanya 31 file `.class` (bytecode Java dikompilasi di image); semua JS/HTML/CSS identik (`app.min.js` `fe589306…`, `DiffSync.js` `fa888c7b…`) | `diff` hash |
| Plugin / embed / diff / patch / export / save-load identik antar arch? | embed, plugin host-injected, diff, patch: identik (E1–E9). Export server-side: tidak ada (`EXPORT_URL=null`). Save/load XML: round-trip stabil (§10) | E1–E9, checksum |
| Editor mencoba akses Internet? | **tidak**: dengan semua request non-localhost diblokir, 0 origin eksternal diminta, edit + patch tetap bekerja (E11) | E11 |
| Kerentanan | Trivy HIGH/CRITICAL: 4 HIGH semuanya di `usr/bin/pebble` (stdlib Go 1.27.1; komponen ACME tak dipakai), set identik arm64/amd64; 0 di paket OS dan Java | `trivy-*.json` |
| Reproduksibilitas tag | Dockerfile upstream meng-clone HEAD `jgraph/drawio` saat build (branch dev) — INFERENCE untuk image terpublikasi → **pin digest, bukan tag** | `docker-drawio/main/Dockerfile` |

## 6. Docker results
- `docker version/info/buildx ls/inspect` valid; platform `linux/arm64`, `linux/amd64` tersedia (EVIDENCE `environment.txt`).
- Build multi-platform satu Dockerfile (`docker buildx build --platform linux/amd64,linux/arm64`, tanpa push) berhasil: manifest-list `sha256:a42c6503…`; pola sama dengan `n8n-custom-service/Dockerfile`.
- Audit asumsi arsitektur: tidak ada binary x86, library native, atau skrip bergantung arsitektur di kode spike. `go version -m` kedua binary: `CGO_ENABLED=0`, `GOOS=linux`, `GOARCH=arm64|amd64`; `file`: *statically linked*. Dependensi: stdlib + `pgx v5.11.0` + `x/text 0.29.0` (Go murni).
- Promosi registry: push ke `registry:2` lokal dan salinan registry→registry mempertahankan **digest index yang sama** (`3d0a5a5f…`) dan kedua platform (EVIDENCE). Harbor/tar air-gapped: UNVERIFIED.

## 7. Matriks hardening container (draw.io 32.6.1)
Skrip `spikes/spike-006-crossbuild/harden.sh`, hasil `harden.out`.

| Skenario | Platform | HTTP | Dilayani di | `DRAWIO_*` diterapkan? | Catatan |
|---|---|---|---|---|---|
| default (uid 1001/gid 999) | arm64 | 200 | `/editor/` | ya | |
| `--user 1234:0` | arm64 | 200 | `/editor/` | ya | UID acak + GID 0 (pola OpenShift/K8s `runAsGroup:0`) |
| `--user 1234:1234` | arm64 | 200 | `/` | **tidak** | WARNING "No write access" → seluruh konfigurasi runtime dilewati diam-diam |
| `--read-only` + tmpfs logs/temp/work | arm64 | 200 | `/` | **tidak** | sama; hanya satu baris WARNING |
| `--cap-drop ALL --security-opt no-new-privileges --user 1234:0` | arm64 | 200 | `/editor/` | ya | |
| `--user 1234:0` | amd64 | 200 | `/editor/` | ya | |
| cap-drop ALL + no-new-privileges + `--user 1234:0` | amd64 | 200 | `/editor/` | ya | |

Implikasi: probe/readiness harus memeriksa `GET /editor/`, bukan `/`; `runAsGroup: 0`; **jangan** `readOnlyRootFilesystem` (ADR-0012).

## 8. UTM results
- VM yang ada: 8 VM lab aarch64 (RHEL 9.8) yang sedang menjalankan lab; **tidak ada VM AMD64**. Membuat VM x86-64 di UTM berarti emulasi QEMU penuh (lambat) dan tidak akan membuktikan RHEL/RKE office; **tidak dibuat** (sesuai arahan "jangan dipaksakan").
- Fakta yang dikumpulkan dari VM lab (read-only): RHEL 9.8, aarch64, RKE2 v1.33.13, SELinux Permissive. Ini memberi bukti parsial untuk "RHEL 9.8 + RKE-family" tetapi **arsitektur salah** dan **SELinux tidak enforcing**.
- Status: validasi AMD64 via UTM = **NOT PERFORMED (gap dijelaskan)**; pengganti = container amd64 (§4).

## 9. Go results
| Uji | Hasil |
|---|---|
| `go vet` semua spike | bersih |
| CGO | empat modul spike dibangun dengan `CGO_ENABLED=0` OK; `go list -deps` (CGO=0) tidak menemukan satu pun paket ber-CgoFiles. Dependensi non-stdlib hanya di `spike-005-pg`: `pgx/v5 v5.11.0`, `puddle/v2`, `pgpassfile`, `pgservicefile`, `golang.org/x/text`, `x/sync` (semua Go murni) |
| Biaya CPU op log (`spike-002-oplog`): sequencing+encode+apply LWW | **dua run, beban mesin berbeda:** run sibuk (Mac berbagi CPU) p99 18–29 µs/op, 10.000 op = 71 ms, replay 1 juta op ±165 ms, 200 dokumen paralel ±1,49 juta op/s; run tenang p99 2–5 µs/op, 10.000 op = 11 ms, replay 1 juta op ±14 ms, ±6,5 juta op/s. Klaim yang dipakai: **p99 ≤ 29 µs/op** (batas atas), CPU bukan bottleneck |
| Determinisme lintas arsitektur | fuzz seed sama arm64 vs amd64: identik (§11) |
| Fractional index | 200.000 sisipan acak valid; kasus terburuk: 2.000 sisipan di titik sama → kunci 334–401 karakter (pertumbuhan ±1 karakter per 5–6 sisipan) → butuh `rebalanceSiblings` (ADR-0010) |

## 10. Checksum results (CANON-v0, `spikes/spike-003-checksum`)
Spesifikasi di header `canon.js`; JS membaca model **hidup** mxGraph, Go membaca **XML**.

| Uji | Hasil |
|---|---|
| Angka: `num()` JS vs Go pada 199.992 double (tepi: `0.03125`, `1e21`, `1e-7`, `5e-324`, max float, `-0`) | **0 selisih** |
| Dokumen tulisan tangan (24: unicode/emoji/RTL/NFC-NFD, entitas, `\|` dan `\` di nilai, kosong vs absen, UserObject/object, urutan style, duplikat style, geometry default eksplisit, angka `1e2`/`0050`/`1e-7`, multi-page, grup/container, `alternateBounds`, waypoint/sourcePoint/offset, hidden/non-connectable, terminal menggantung, HTML label, urutan sibling) | JS(live) = Go(XML ekspor draw.io) di **24/24**; = Go(XML mentah) di **23/24** — satu-satunya perbedaan `numbersNonCanonical` (`5px`, ` 7`): Go **sengaja menolak** angka tidak kanonik, draw.io `parseFloat` menoleransi lalu mengekspor kanonik (temuan 3 di bawah). Sebelum normalizer atribut (temuan 2) ada dua perbedaan |
| 150 dokumen acak (25 edit acak di editor hidup) | JS(live) = Go(ekspor) **150/150** |
| Round-trip ekspor→muat→ekspor stabil | 5/5 |
| Korpus yang sama (174 dokumen) dihitung oleh binary Go **linux/arm64** dan **linux/amd64** | **174/174** = hash JS |
| Browser berbeda (Chrome Mac arm64 vs Chromium Linux x86_64) | hash JS identik di 24/24 dokumen deterministik; di Chromium sendiri 150/150 dan 23/24 sama seperti di Mac. (Dokumen acak tidak bisa dibandingkan lintas run: draw.io membuat ID acak untuk grup/page) |

Temuan (semua nyata, sudah dikodekan):
1. Setelah decode XML, flag `visible`/`connectable`/`collapsed` berupa **angka 0/1**, pada cell default berupa boolean → `!== false` salah; wajib koersi truthiness.
2. `encoding/xml` Go **tidak** menormalisasi tab/CR/LF literal di atribut (XML §3.3.3), browser menormalisasi → perlu normalisasi sebelum parse (diimplementasikan) agar sama.
3. `parseFloat` JS toleran (`"5px"` → 5); Go **sengaja menolak** angka tidak kanonik. Ekspor draw.io selalu kanonik.
4. Page non-aktif dimuat malas (`root` null) → `updatePageRoot` untuk semua page sebelum hash.
5. `UserObject`/`object` membungkus `mxCell`; `id` ada di pembungkus.
6. Checksum bawaan draw.io (`getHashValueForPages`) konsisten antar replika (EVIDENCE E3–E5) tetapi **tidak dapat dihitung server Go**; checksum platform = CANON-v0.
Batasan: itu validasi *representasi*; pencocokan penuh dengan model Go yang bertindak sebagai sumber kebenaran (round-trip XML→model→XML) tetap pekerjaan Phase 5 (R2-1).

## 11. Fuzz / property test (`spikes/spike-004-fuzz`, Go)
Simulasi deterministik: 1 sequencer server, 3–5 klien dengan state optimistis (confirmed + pending di-re-apply), link FIFO ber-delay, offline/reconnect + catch-up, retransmit (duplikat), izin yang dicabut/diberikan di tengah jalan, nack. Sumber kebenaran independen: model referensi absolut-koordinat.

Properti: konvergensi semua replika; idempotensi (log diterapkan dua kali); replay = state server (+ snapshot-resume); tidak ada siklus/orphan/terminal mati/kunci urutan tidak valid; tidak ada *lost update* (nilai akhir = penulis terakhir menurut urutan server); posisi akhir = intent absolut penulis (*wrong intent*); checksum per-seq sama di klien; izin (tidak ada op yang di-sequence saat dicabut); akuntansi (tiap op berakhir applied **atau** nacked, dan nack sampai ke penulis); determinisme (run ulang = hasil sama).

| Kampanye | Skrip | Op dihasilkan | Gagal | Non-deterministik | Catatan |
|---|---|---|---|---|---|
| **INTENT** (kontrak ADR-0010 revisi) | 12.000 | 1.153.381 | **0** | **0** | 790.980 diterapkan; 114.015 nack `forbidden`; 223.729 duplikat diabaikan; 47.370 periode offline; 3.024 overwrite konkuren |
| NAIVE (geometri relatif ala patch native) | 3.000 | 292.562 | 0 | 0 | **1.661 pelanggaran wrong-intent** (INTENT: 0) |
| Mutasi `nocycle` | 600 | – | **74** | – | siklus + wrong-intent tertangkap |
| Mutasi `nocascade` | 600 | – | **290** | – | orphan/ancestor mati tertangkap |
| Mutasi `unstablesort` | 600 | – | **550** | 293 | divergensi/checksum mismatch tertangkap |
| Mutasi `noauthz` | 600 | – | **413** | – | pelanggaran izin tertangkap |
| Mutasi `silentdrop` (op hilang tanpa ack/nack) | 600 | – | **600** | – | akuntansi tertangkap, di-shrink ke 1 event |
| Mutasi `dropack` (ack hilang) | 600 | – | 0 | 0 | **bukan celah fuzzer**: deteksi celah sequence + `sync` menyembuhkan; ini bukti ketahanan protokol |

Bug nyata yang ditemukan fuzzer pada **desain saya sendiri** (dan perbaikannya, sekarang di ADR-0010):
1. *Idempotensi gagal* (28/300 skrip, di-shrink 7–9 event): konversi absolut→relatif dihitung saat apply → perbaikan: **log menyimpan op resolved**.
2. Non-determinisme simulator (iterasi `map` saat re-kirim nack) – perbaikan di simulator, bukan model.
Fixture regresi hasil shrink: `spikes/spike-004-fuzz/results/regressions/`.

Hasil lintas arsitektur (binary linux/arm64 native vs linux/amd64 emulasi, seed sama): identik (§4).
Batas klaim: ini memvalidasi **model dan aturan merge di Go**, bukan integrasi draw.io↔adapter↔server (R2-2/R2-3 integrasi masih UNVERIFIED), dan hanya 2 halaman awal + id unik per dokumen.

## 12. PostgreSQL results (`spikes/spike-005-pg`)
Metodologi: tabel `ops` PK `(document_id, server_seq)` + unique `(document_id, operation_id)`; sequencer per dokumen; transaksi `UPDATE documents SET head_seq=head_seq+n … RETURNING` + `INSERT … SELECT … FROM unnest(...)`; open-loop pada laju target; latensi = enqueue→commit; warm-up 4 dtk + `CHECKPOINT` tiap run; pool 32; payload ±270 B. Tabel lengkap di ADR-0007 §"Bukti benchmark".

Ringkas: 100 op/s p99 7 ms; tanpa batching satu dokumen jenuh ±934 op/s; group commit 2 ms menopang 9.946 op/s dengan p99 8,1 ms pada satu dokumen; 200 dokumen tanpa batching jenuh 6.724 op/s (pool 32); 440 B/op; replay 100.000 op 167 ms, 1 juta op 1,6 s; snapshot 40 KB p50 1,0 ms; DELETE 500.000 baris 266 ms tetapi ukuran tabel tidak turun → partisi.
**Batas:** disk Docker Desktop; angka absolut harus diulang di VM PostgreSQL lab dan server office dengan program yang sama. Contention banyak penulis pada **satu dokumen** diukur (1 sequencer goroutine); beberapa replika service per dokumen belum diuji.

## 13. E2E / browser results
Semua via protokol embed dengan dua/tiga iframe editor dan relay pesan. Detail E0–E11 di SPIKE-001 + hasil JSON.

| Eksperimen | Hasil | Status |
|---|---|---|
| E1 plugin | host-injected `Draw.loadPlugin` PASS; `?p=` kustom FAIL (`Unknown plugin`); settings plugin butuh `ALLOW_CUSTOM_PLUGINS` | PASS (jalur host) |
| E2 capture 15 jenis operasi | semua menghasilkan `autosave` + patch (kecuali select page) | PASS |
| E3 replay 11 patch (+2 undo) ke replika lain | state identik | PASS |
| E4 remote apply | 0 echo; undo lokal utuh; seleksi/page utuh; duplikat idempoten | PASS |
| **E8 edit teks in-place saat patch remote masuk** | editor tetap terbuka dan teks ketikan tetap (patch geometri/label sel sama/label sel lain); saat `stopEditing` teks lokal menang *diam-diam* atas label remote (label remote hilang) dan B mengirim autosave sebagai LOCAL; bila sel yang diedit **dihapus** remote, editor menutup tanpa error dan teks ketikan hilang | CONDITIONAL (perilaku jelas; konflik harus dimunculkan oleh platform; posisi caret tidak diuji) |
| **E9 undo setelah patch remote** | U1 (remote move, undo label lokal) benar; **U2 (remote ubah label yang sama) → undo lokal menimpa label remote ke nilai lama** (undo menjadi op LOCAL baru → wrong intent); U3 (add lokal, delete remote, undo) tanpa error, tidak menghidupkan lagi | CONDITIONAL — undo harus per-actor + dilaporkan sebagai overwrite |
| E10 bottleneck | lihat §14 #11 | PASS (terukur) |
| E11 air-gapped | 0 origin eksternal; editor + patch jalan | PASS |

## 14. Re-verifikasi klaim SPIKE-001 (17 butir)
| # | Klaim | Hasil re-verifikasi | Status |
|---|---|---|---|
| 1 | `diffPages` | direproduksi pada image sungguhan (arm64+amd64, 2 browser); format `u/i/r` per cell/properti; `style`/`geometry`/`xmlValue` blob | PASS |
| 2 | `patchPages` | via action `patch`: apply, ack + checksum, idempoten | PASS |
| 3 | native diff/patch sebagai batas integrasi | tetap CONDITIONAL (tanpa schema resmi; blob → lost update) | CONDITIONAL |
| 4 | plugin tanpa patch image | host-injected PASS; ditegaskan tidak butuh `ALLOW_CUSTOM_PLUGINS` | PASS |
| 5 | remote apply | PASS (E4) | PASS |
| 6 | undo | undo lokal tetap benar setelah patch remote; **tetapi** undo menimpa edit remote pada sel yang sama (U2) | CONDITIONAL (klaim lama "undo aman" dikoreksi) |
| 7 | echo prevention | PASS (0 autosave dari patch) | PASS |
| 8 | duplicate delivery | PASS di draw.io; juga di model Go | PASS |
| 9 | checksum | checksum draw.io konsisten; CANON-v0 platform terbukti 174/174 | PASS (lihat §10) |
| 10 | conflict scenarios | direproduksi; **koreksi: relay naif divergen 10/18, bukan 9/18**; S3 15/18; S3+repair 18/18 | PASS (angka dikoreksi) |
| 11 | performa diagram besar | **klaim sebab dikoreksi**: lihat di bawah | PASS terukur / sebab dikoreksi |
| 12 | operasi struktural | diuji di draw.io (E5) dan model Go (fuzz INTENT); integrasi adapter belum | CONDITIONAL |
| 13 | urutan page | add-page ganda divergen tanpa repair; fractional index di model: PASS | CONDITIONAL |
| 14 | z-order | konvergen di E5; model fractional index PASS | PASS |
| 15 | cascade delete | E5 divergen tanpa repair, repair konvergen; model Go PASS | CONDITIONAL |
| 16 | reparent/group | E5 "niat salah" pada group-vs-move; model INTENT 0 pelanggaran | CONDITIONAL |
| 17 | text editing | E8 (baru) | CONDITIONAL |

**Koreksi #11 (E10, arm64 image + Chrome Mac):**
| Sel | Update graph murni draw.io (autosave mati) | Dengan jalur autosave+diffSync bawaan | Op→event `autosave` di host | clone | hash | diff | serialisasi XML |
|---|---|---|---|---|---|---|---|
| 50 | 6,6 ms | 7,7 ms | 11,8 ms | 0,3 | 0,5 | 0,3 | 0,3 |
| 500 | 15,6 ms | 31 ms | 40 ms | 2,8 | 3,2 | 2,0 | 1,8 |
| 5.000 | **116,8 ms** | 252,6 ms | 337,8 ms | 30,6 | 34,2 | 19,1 | 18,0 |

Klaim lama "biaya terutama dari getData/diff/checksum" **tidak lengkap**: pada 5.000 cell ±46% adalah update graph draw.io sendiri (tak bisa dikurangi tanpa mengubah draw.io), ±40% jalur sync bawaan (clone+hash+diff+XML ≈ 100 ms), sisanya pesan. Lantai tanpa sync = 117 ms, jadi target <100 ms pada 5.000 cell **tidak realistis**; di 500 cell 28 ms (PASS). Batas nyaman realtime ±1.000 cell (INTENT: interpolasi 500→5.000).

## 15. Failures / koreksi yang ditemukan
| # | Temuan | Dampak | Tindakan |
|---|---|---|---|
| F1 | Angka SPIKE-001: relay naif divergen **10/18** (ditulis 9/18) | dokumen salah | dikoreksi di SPIKE-001, MASTER-PLAN, ADR-0006 |
| F2 | Penjelasan bottleneck SPIKE-001 tidak lengkap | klaim salah | dikoreksi (§14 #11) |
| F3 | Idempotensi model hipotesis gagal (konversi saat apply) | desain berubah | op resolved di log (ADR-0010) |
| F4 | Ekstraktor JS salah baca flag numerik 0/1 | checksum beda | diperbaiki, 6 gotcha tercatat (§10) |
| F5 | Go `encoding/xml` tidak menormalisasi atribut | checksum beda | normalizer ditambah |
| F6 | Image draw.io melewati semua `DRAWIO_*` bila rootfs read-only/GID≠0 | service salah path senyap | syarat securityContext + probe `/editor/` (ADR-0012) |
| F7 | Undo menimpa edit remote (U2); edit in-place menimpa label remote (E8) | wrong intent | event `overridden` + undo per-actor (ADR-0010) |
| F8 | Suite Chromium x86 pertama time-out (berebut CPU dengan fuzz) | hasil tertunda | timeout dinaikkan, dijalankan ulang tanpa beban: PASS |
| F9 | Mutasi `silentdrop` awal tidak efektif (kondisi saya salah) | uji palsu-lolos | diperbaiki: tertangkap 600/600 |

## 16. Unknowns (belum dibuktikan)
- Perilaku sebenarnya di **RHEL 9.6/9.8 AMD64 + RKE** (§17), SELinux enforcing, runtime container kantor, CNI/NetworkPolicy, ingress.
- Kinerja pada x86 server dan pada VM PostgreSQL lab/office; contention multi-replika pada satu dokumen.
- Integrasi penuh adapter (patch native↔semantic op dua arah) dengan model Go sebagai sumber kebenaran; fuzz tingkat integrasi (draw.io di loop).
- Round-trip XML→model Go→XML pada korpus diagram nyata (≥ 20); checksum CANON-v0 terhadap fitur draw.io yang jarang (mis. `alternateBounds` bersarang, gaya khusus, kompresi `diagram` — dekompresi ada di Go, belum diuji dengan korpus terkompresi dari draw.io).
- Posisi caret/seleksi teks saat patch remote masuk (E8 hanya memeriksa isi).
- Promosi image ke Harbor kantor dan transfer tar air-gapped; SBOM/signing.
- Klien S3 (SeaweedFS) dan Redis di kedua arsitektur (belum ada kode; lib Go murni diharapkan — INFERENCE).
- Model AI on-prem yang tersedia di office.

## 17. Matriks uji lintas arsitektur
| Komponen | ARM64 | AMD64 | Bukti |
|---|---|---|---|
| draw.io container | PASS (native) | PASS (emulasi; fungsional) | E0–E11, hardening |
| gateway | NOT APPLICABLE (belum ada kode) | NOT APPLICABLE | stand-in `server.mjs` (spike) |
| document service | NOT APPLICABLE | NOT APPLICABLE | – |
| collab service | PARTIAL (model Go: fuzz PASS) | PARTIAL (identik, emulasi) | spike-004 |
| mcp server | NOT APPLICABLE | NOT APPLICABLE | – |
| PostgreSQL client | PASS | PASS fungsional (waktu tidak sah) | spike-005 |
| S3 client | UNVERIFIED | UNVERIFIED | belum ada kode |
| Redis client | UNVERIFIED | UNVERIFIED | belum ada kode |
| browser E2E | PASS (Chrome 155/156) | PASS (Chromium 153 x86_64) | suite E0–E11 |
| semantic ops | PARTIAL (model; adapter belum) | PARTIAL | spike-004 |
| checksum | PASS (174/174) | PASS (174/174; Chromium 150/150) | spike-003 |
| op-log benchmark | PASS (CPU + PG) | PARTIAL (CPU/logika identik; waktu tidak representatif) | spike-002/005 |
| fuzz test | PASS | PASS (identik) | spike-004 |
| RHEL 9.x | PARTIAL (lab RHEL 9.8 aarch64 dibaca; tidak menjalankan workload) | UNVERIFIED | ssh read-only |
| RKE | PARTIAL (RKE2 1.33 lab terbaca; tidak menjalankan workload) | UNVERIFIED | ssh read-only |

## 18. Gap RHEL/RKE dan yang wajib diuji di sana
**Tidak bisa dibuktikan di Mac / UTM:**
1. SELinux **enforcing** (lab Permissive): label konteks `container_t`, `emptyDir`, perizinan file draw.io (GID 0), probe.
2. Runtime container kantor (RKE1/Docker vs RKE2/containerd): perilaku `securityContext`, `seccompProfile: RuntimeDefault`, drop capabilities.
3. CNI/NetworkPolicy, ingress-nginx (versi, WebSocket upgrade, timeout idle, `proxy-body-size`), DNS in-cluster, MTU.
4. Harbor kantor: manifest-list multi-arch, replikasi/impor tar air-gapped, pin digest, retensi.
5. Kinerja x86 server (CPU, fsync disk, PostgreSQL kantor), kuota resource, JVM container-awareness (cgroup v2).
6. CA internal (Keycloak/PostgreSQL/SeaweedFS TLS), NTP/jam node (OIDC `exp/nbf`).
7. Mirror internal untuk pull image draw.io; tidak ada egress.

**Bisa dibuktikan penuh di Mac (selesai):** semantic operation/merge/checksum/replay (Go, deterministik lintas arsitektur), perilaku editor (webapp identik), kontrak embed, build multi-arch, matriks securityContext dasar, benchmark relatif PostgreSQL.
**Bisa dibuktikan sebagian dengan AMD64 (emulasi/UTM):** kebenaran fungsional binary amd64, start container, Chromium x86-64.
**Tetap butuh RHEL/RKE sungguhan:** semua butir 1–7 di atas. Rencana uji: Phase 1 smoke di cluster kantor (draw.io saja) memuat daftar ini sebagai acceptance criteria.

## 19. Rekomendasi
1. Arsitektur **portable by design**: logika/kontrak/merge/persistence/MCP identik; hanya build/runtime/deploy yang berbeda (ADR-0011).
2. Mulai **Phase 1** (deploy draw.io asli) dengan syarat di MASTER-PLAN (digest pin, securityContext GID 0, probe `/editor/`, mirror internal, smoke di cluster kantor sebelum menaruh kode custom).
3. **Jangan** memulai Phase 5 (kolaborasi) sebelum SPIKE-003 (integrasi adapter + model Go + fuzz tingkat integrasi + round-trip korpus nyata).
4. Ubah ADR: 0005 (2 deployable → 3 di Phase 6), 0007 (PG diterima, partisi), 0010 (op resolved, intent absolut, overridden, rebalance), 0011/0012 baru.
5. Ulangi benchmark PostgreSQL di VM lab dan office dengan program `spike-005-pg` yang sama.

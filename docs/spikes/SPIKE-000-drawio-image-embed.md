# SPIKE-000 — Image `jgraph/drawio` dan dasar embed

Author / Agent: Claude Code
Timestamp: 2026-10-24 23:55
Status: **Selesai sebagian** (pemeriksaan sumber image + runtime webapp; image Tomcat belum dijalankan karena Docker Desktop mati)

Label bukti: **EVIDENCE** = dari source code / dokumentasi resmi / eksperimen yang benar-benar dijalankan;
**INFERENCE** = penalaran dari pengetahuan umum; **UNKNOWN** = belum dibuktikan.

## Pertanyaan dan jawaban

| # | Pertanyaan | Jawaban | Status | Bukti |
|---|---|---|---|---|
| 1 | Ada image `linux/arm64` (node lab aarch64)? | Ya, `32.6.1` dan tag sebelumnya punya `amd64` + `arm64` | PASS | EVIDENCE: Docker Hub API `GET /v2/repositories/jgraph/drawio/tags` → `32.6.1 ['amd64','arm64']`, `32.3.0 ['amd64','arm64']` |
| 2 | Isi image = webapp rilis resmi? | Ya: Dockerfile meng-clone `jgraph/drawio`, `ant war`, unzip `draw.war` ke `webapps/draw`, Tomcat 9 JDK 11, context `/` | PASS | EVIDENCE: `jgraph/docker-drawio/main/Dockerfile` |
| 3 | Bisa dilayani di subpath `/editor/`? | Webapp memakai path relatif; editor termuat penuh di `/editor/` di belakang server lain, mode embed berjalan | PASS (webapp) | EVIDENCE runtime: E0 `editorPath: "/editor/"`, editor siap + `init`/`load` diterima. Image sendiri juga punya dukungan subpath (`DRAWIO_SERVER_URL` → ubah Tomcat context) — EVIDENCE entrypoint baris 60–75 |
| 4 | Konfigurasi tanpa mengubah image | Semua lewat env → `docker-entrypoint.sh` menulis `js/PreConfig.js` saat start: `DRAWIO_BASE_URL`/`DRAWIO_SERVER_URL`, `DRAWIO_CONFIG`/`DRAWIO_CONFIG_FILE`, `DRAWIO_CSP_HEADER`, `DRAWIO_LANG`; integrasi cloud mati bila client id kosong (`gapi/od/gl=0`, `db/gh/tr=0` selalu); `urlParams['sync']='manual'` (realtime bawaan mati); `EXPORT_URL=null` | PASS | EVIDENCE: entrypoint baris 28–221 |
| 5 | Root filesystem harus writable? | Entrypoint menulis `PreConfig.js`, `PostConfig.js`, `server.xml`. Bila tidak bisa menulis, **seluruh** konfigurasi runtime dilewati (warning, start dengan konfigurasi bawaan) | PASS (diketahui) | EVIDENCE: entrypoint baris 15–26. Konsekuensi: `readOnlyRootFilesystem: false`, atau mount `PreConfig.js` dari ConfigMap (entrypoint lalu melewati konfigurasi runtime; context path tetap `/`) |
| 6 | User / UID | `tomcat` uid 1001, gid 999; berjalan dengan UID arbitrer asal GID 0 | PASS | EVIDENCE: Dockerfile (`useradd -u 1001`, `chmod -R g=u`) |
| 7 | CSP default | Dipasang sebagai `<meta http-equiv>` oleh `PreConfig.js`: `default-src 'self'; script-src 'self' … 'unsafe-inline'; connect-src 'self' …; frame-src 'self' https://*.google.com` | PASS | EVIDENCE: entrypoint baris 29–42; runtime E0: fetch same-origin `ok 200`, cross-origin diblokir (`securitypolicyviolation connect-src`), `new WebSocket(ws://same-origin)` tanpa violation |
| 8 | Bisa di-iframe oleh host same-origin? | Ya; CSP via `<meta>` tidak mendukung `frame-ancestors`, jadi pembatasan embedding harus di header gateway | PASS (webapp) | EVIDENCE runtime E0/E1 (iframe same-origin). Header Tomcat/`X-Frame-Options` dari image: **UNKNOWN** (image belum dijalankan) |
| 9 | Protokol embed resmi | `init/load/autosave/merge/patch/getDiff/resetDiff/remoteInvoke/export/...` terdokumentasi di drawio.com embed-mode | PASS | EVIDENCE: dokumentasi resmi (dibaca 2026-10-24) + `EditorUi.js` handler `data.action == ...` |
| 10 | Export server-side PNG/PDF | Tidak ada di image (`EXPORT_URL=null`); PDF via dialog print, PNG/SVG di klien | PASS (diketahui) | EVIDENCE: entrypoint baris 83–87 |

## Implikasi untuk Phase 1 (deploy draw.io asli)
- Tag dipin dan berumur ≥ 2 minggu saat deploy (kandidat sekarang: `32.3.0`, 2026-10-07), arsitektur multi-arch sudah terbukti.
- `securityContext`: `runAsUser` boleh arbitrer dengan `runAsGroup: 0` / `supplementalGroups: [0]`; `readOnlyRootFilesystem: false` (atau varian ConfigMap `PreConfig.js` — diputuskan saat Phase 1, dicatat di ADR-0001/0002).
- `DRAWIO_BASE_URL=https://drawio.lab.local/editor` + `DRAWIO_SERVER_URL=https://drawio.lab.local/editor/` (Tomcat context jadi `/editor`), gateway mem-proxy `/editor/*` tanpa strip path. INFERENCE: konsisten dengan entrypoint, diuji di Phase 1.
- `DRAWIO_CSP_HEADER` diset eksplisit (hapus domain Google/Dropbox/GitHub yang tidak dipakai; air-gapped) dan `frame-ancestors 'self'` dipasang gateway sebagai header.
- Tanpa PVC: image stateless (tidak ada data user di disk). EVIDENCE: tidak ada `VOLUME` di Dockerfile.

## Belum dibuktikan (dibawa ke Phase 1)
- Menjalankan image Tomcat di cluster lab (arm64) + header respons nyata (`X-Frame-Options`, cache) — UNKNOWN.
- Waktu start, memori Tomcat/JVM, probe — UNKNOWN.

## Cara mengulang
Lihat `spikes/README.md` (unduh `draw.war` v32.6.1, sha256 `44dd5695…5e42aa`) dan `spikes/spike-001/run.mjs` (eksperimen E0/E1).

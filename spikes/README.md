# spikes/ — prototipe buang-pakai

> **DISPOSABLE SPIKE — NOT PRODUCTION CODE.** Semua isi folder ini adalah eksperimen sekali pakai untuk membuktikan atau menolak hipotesis arsitektur
> (lihat `docs/spikes/`). Tidak dibangun Jenkins, tidak di-deploy, tidak boleh di-import oleh kode produksi, dan boleh dihapus setelah temuan dicatat.
> Hasil mentah (JSON/log) disimpan agar klaim di dokumen bisa diperiksa ulang.

Author / Agent: Claude Code
Timestamp: 2026-10-25 03:15

| Folder | Menjawab | Dokumen |
|---|---|---|
| `spike-001/` | change model draw.io: embed `diffSync`/`patch`, plugin, remote apply, undo, konflik, performa, air-gapped (E0–E11); harness Playwright terhadap `draw.war` **atau** container `jgraph/drawio` | SPIKE-001, SPIKE-002 §13–§14 |
| `spike-002-oplog/` | biaya CPU op log (sequencing, encode, LWW apply, replay) | SPIKE-001 §8, SPIKE-002 §9 |
| `spike-003-checksum/` | checksum kanonik CANON-v0: JS (model hidup) ↔ Go (XML); 199.992 angka; 174 dokumen | SPIKE-002 §10 |
| `spike-004-fuzz/` | fuzz/property test model kolaborasi di Go (sequencer, klien, offline, duplikat, izin) + mutasi + shrinker | SPIKE-002 §11 |
| `spike-005-pg/` | benchmark op log PostgreSQL (append, group commit, replay, snapshot, kompaksi) | SPIKE-002 §12, ADR-0007 |
| `spike-006-crossbuild/` | satu Dockerfile → linux/arm64 + linux/amd64; matriks hardening container; Trivy; lingkungan | SPIKE-002 §4–§7 |

## Prasyarat umum
Docker Desktop (buildx), Go ≥ 1.27, Node ≥ 20, Google Chrome (atau Chromium dari Playwright). Webapp draw.io: `draw.war` rilis v32.6.1 (sha256 `44dd56958c8408963457dc98f04d2228408ac145c7aa1d8e6895d9c88d5e42aa`) diekstrak di direktori terpisah (data tak tepercaya), atau container `jgraph/drawio:32.6.1`.

## Cara menjalankan (ringkas)
```bash
# 1) harness browser (E0–E11) terhadap image sungguhan, dua arsitektur
docker run -d --name d-arm --platform linux/arm64 -p 127.0.0.1:8081:8080 \
  -e DRAWIO_SERVER_URL=http://localhost:8790/editor/ -e DRAWIO_BASE_URL=http://localhost:8790/editor jgraph/drawio:32.6.1
(cd spike-001 && npm install && RUN_TAG=image-arm64 DRAWIO_UPSTREAM=http://127.0.0.1:8081 node run.mjs)
python3 spike-001/compare.py spike-001/results/results-image-arm64.json spike-001/results/results-image-amd64.json

# 2) Chromium x86-64 (container amd64), memakai webapp statis
docker run --rm --platform linux/amd64 --ipc=host -v "$PWD":/work/spikes -v "$WEBAPP":/webapp:ro -e DRAWIO_WEBAPP=/webapp \
  -e CHROME=bundled -e RUN_TAG=chromium-amd64 -w /work/spikes/spike-001 mcr.microsoft.com/playwright:v1.63.0-noble node run.mjs

# 3) checksum, fuzz, PostgreSQL
(cd spike-003-checksum && go build -o /tmp/spike003-canon . && node run-nums.mjs && DRAWIO_WEBAPP=$WEBAPP node run.mjs)
(cd spike-004-fuzz && go build -o /tmp/spike004-fuzz . && ./run-all.sh)
(cd spike-005-pg && PG_DSN=postgres://postgres:spike@127.0.0.1:55432/spike go run . -out results.json)   # DB sekali pakai!

# 4) build multi-platform tanpa push + matriks hardening
docker buildx build --platform linux/amd64,linux/arm64 -f spike-006-crossbuild/Dockerfile -t spike/crossbuild:test .
./spike-006-crossbuild/harden.sh
```
`spike-005-pg` melakukan `DROP TABLE` pada tabelnya sendiri: **jangan arahkan ke database yang berisi data**.

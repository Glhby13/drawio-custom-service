# drawio-custom-service

Author / Agent: Claude Code
Timestamp: 2026-10-25 03:15

Lapisan enterprise di sekitar **draw.io Community** (image `jgraph/drawio` dipakai apa adanya): SSO Keycloak, penyimpanan diagram terpusat
(SeaweedFS S3 + PostgreSQL), izin/share, versi, kolaborasi, audit, MCP, dan AI sebagai kolaborator — tanpa mengubah draw.io dan tanpa PVC.
Pola repo dan deploy mengikuti `n8n-custom-service`. Satu sumber untuk lab (ARM64) dan office (AMD64).

**Status (2026-10-25, lab-first): Phase 0–8 selesai di lab — SSO, penyimpanan terpusat (PostgreSQL + SeaweedFS + Redis, tanpa PVC), izin/share, kolaborasi realtime, riwayat/revert, versi, komentar, ekspor, audit, MCP server, dan AI sebagai kolaborator terotorisasi. Deploy GitOps (Jenkins → Harbor → ArgoCD) dan 3 suite E2E Chrome nyata lulus (25 + 44 + 43 cek). Kantor RHEL/RKE AMD64: UNVERIFIED (langkah: `lab/docs/office-sync/DRAWIO-PHASE2-8-OFFICE-STEPS.md`).**

| Dokumen | Isi |
|---|---|
| [docs/MASTER-PLAN.md](docs/MASTER-PLAN.md) | rencana induk, roadmap per fase, status matrix, keputusan review |
| [docs/adr/](docs/adr/README.md) | ADR-0001 … ADR-0012 |
| [docs/spikes/SPIKE-000-drawio-image-embed.md](docs/spikes/SPIKE-000-drawio-image-embed.md) | image & dasar embed |
| [docs/spikes/SPIKE-001-drawio-change-model.md](docs/spikes/SPIKE-001-drawio-change-model.md) | change model, konflik, benchmark (dengan koreksi) |
| [docs/spikes/SPIKE-002-cross-platform.md](docs/spikes/SPIKE-002-cross-platform.md) | ARM64 ↔ AMD64, re-verifikasi, fuzz, checksum, PostgreSQL, gap RHEL/RKE |
| [spikes/](spikes/README.md) | prototipe buang-pakai (**BUKAN KODE PRODUKSI**) |

## Kode produksi

| Path | Isi |
|---|---|
| `cmd/gateway`, `internal/edge`, `internal/identity`, `internal/session`, `internal/config` | gateway: OIDC (code + PKCE, verifikasi JWKS ID token & access token), sesi server-side di Redis, CSRF/origin, rate limit, proxy `/editor/*` ke draw.io resmi, host page + adapter stub, token internal EdDSA ≤ 5 menit |
| `cmd/platform`, `internal/platform`, `internal/model` | platform: dokumen, PDP, op log berurutan (`FOR UPDATE`), WebSocket kolaborasi (`/ws/v1/documents/{id}`), REST `/api/v1`, versi, komentar, audit, presence; model dokumen + kontrak operasi semantik (ADR-0010) |
| `cmd/mcp-server`, `internal/mcpsrv` | MCP (Streamable HTTP, OAuth bearer Keycloak, 15 tool + resources) → REST platform sebagai aktor AI terdelegasi |
| `internal/edge/web/{model,bridge,adapter,host}.js` | browser: model klien (cermin Go), diff/patch ke draw.io resmi, mesin sinkron, UI (daftar, share, riwayat, versi, komentar, ekspor, presence) |
| `webtest/` | konformansi JS↔Go (vektor dari Go, 1799 kasus diff) |
| `Dockerfile` | satu Dockerfile multi-target (`SERVICE_NAME`), `CGO_ENABLED=0`, scratch, uid 65532 |
| `Jenkinsfile` | lint, test, govulncheck, audit CGO → Kaniko → Harbor `drawio-custom-service` → bump `image.tag` **dan** `image.digest` di helm-values-lab |
| `e2e/` | E2E Chrome nyata terhadap lab: `phase2_gateway.mjs` (25), `phase5_collab.mjs` (44: izin, kolaborasi 3 pengguna, revert/restore, restart pod, revoke), `phase6_mcp.mjs` (43: MCP + AI), `phase7_presence.mjs` (51: presence), `phase8_ui.mjs` (9: dialog, share, riwayat live). Login Keycloak memakai flow `drawio-browser-role-otp` (role `drawio-access` + OTP): suite mendaftarkan TOTP user uji otomatis (seed di `e2e/.otp-seeds.json`, git-ignored). Password uji dibaca dari `lab/SECRETS.md`. `LOCAL_WEB=1` melayani aset host dari working tree (loop cepat tanpa build) |
| `scripts/dev-image.sh` | build multi-arch lokal + push ke Harbor lab lewat bastion (hanya sampai Jenkins membangun repo ini) |
| `deploy/nginx/drawio.lab.local.conf` | vhost nginx lab (TLS edge) satu-satunya jalan masuk |

Gerbang lokal (sama dengan Jenkins): `golangci-lint run ./cmd/... ./internal/...`, `go vet ./...`, `go test -race ./...`, `govulncheck ./...`, audit CGO.
Tidak diwarisi dari n8n-custom-service: `InsecureSkipVerify`, penerimaan ID token tanpa verifikasi tanda tangan.

## Menghubungkan agen AI (MCP)
Endpoint `https://drawio.lab.local/mcp` (Streamable HTTP, JSON-RPC). Metadata OAuth: `/.well-known/oauth-protected-resource` → Keycloak realm `lab`. Satu client Keycloak per aplikasi agen (`scripts/setup_keycloak_mcp.py`: `drawio-mcp-claude` → aktor `ai-claude`); agen masuk sebagai **pengguna sungguhan** dan bekerja atas namanya. Agar agen melihat sebuah diagram, pemilik: (1) Share → centang "Allow AI collaborators", (2) bagikan ke `ai-claude` sebagai editor. Batas: ops/menit dan penghapusan per request diatur pemilik; semua perubahan AI tercatat (aktor, agen, model) dan bisa di-revert dari History.

## Presence langsung (siapa sedang apa)
Saat beberapa orang membuka diagram yang sama, setiap orang melihat **siapa memilih / mengedit / menggeser / mengubah ukuran apa**: kotak berwarna per pengguna + label "Nama · editing", hantu putus-putus saat drag/resize, dan teks yang sedang diketik (draft) di dekat sel. AI yang sedang menerapkan perubahan tampil sebagai "AI … applying changes". Ini **ephemeral** (WebSocket + Redis, TTL 8 dtk, tanpa DB/riwayat/lock): presence memberi tahu, lapisan operasi semantik yang menyelesaikan konflik. Desain/bukti: `docs/spikes/SPIKE-004-live-presence.md`, `docs/adr/0013-live-presence-ephemeral.md`, `e2e/phase7_presence.mjs`. Setelah rilis baru, muat ulang tab yang sudah terbuka sekali.

## Performa
`node e2e/perf_collab.mjs` (lab, 2026-10-25, diagram 500 sel, dua Chrome): server menerima batch 500 `addCell` dalam 87 ms; buka+render 0,7 dtk; latensi perubahan satu sel dari browser A ke B p50 36 ms / p95 42 ms / maks 60 ms (25 sampel; kriteria ADR-0006 #5 p95 < 250 ms **terpenuhi**); ubah gaya 100 sel sekaligus tampil di B dalam 94 ms; hash editor == server setelah semburan. Keterbatasan: satu pasang klien di satu mesin, jaringan lab, belum uji >2 klien aktif menulis bersamaan atau diagram >500 sel.

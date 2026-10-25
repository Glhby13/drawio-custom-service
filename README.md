# drawio-custom-service

Author / Agent: Claude Code
Timestamp: 2026-10-25 03:15

Lapisan enterprise di sekitar **draw.io Community** (image `jgraph/drawio` dipakai apa adanya): SSO Keycloak, penyimpanan diagram terpusat
(SeaweedFS S3 + PostgreSQL), izin/share, versi, kolaborasi, audit, MCP, dan AI sebagai kolaborator — tanpa mengubah draw.io dan tanpa PVC.
Pola repo dan deploy mengikuti `n8n-custom-service`. Satu sumber untuk lab (ARM64) dan office (AMD64).

**Status (2026-10-25, lab-first): Phase 0 selesai · Phase 1 (draw.io resmi) PASS WITH CONDITIONS · Phase 2 (gateway) kode + E2E lab lulus, menunggu persetujuan push · Phase 3+ belum. Kantor RHEL/RKE: UNVERIFIED. Phase 5 tetap terkunci (gate SPIKE-003).**

| Dokumen | Isi |
|---|---|
| [docs/MASTER-PLAN.md](docs/MASTER-PLAN.md) | rencana induk, roadmap per fase, status matrix, keputusan review |
| [docs/adr/](docs/adr/README.md) | ADR-0001 … ADR-0012 |
| [docs/spikes/SPIKE-000-drawio-image-embed.md](docs/spikes/SPIKE-000-drawio-image-embed.md) | image & dasar embed |
| [docs/spikes/SPIKE-001-drawio-change-model.md](docs/spikes/SPIKE-001-drawio-change-model.md) | change model, konflik, benchmark (dengan koreksi) |
| [docs/spikes/SPIKE-002-cross-platform.md](docs/spikes/SPIKE-002-cross-platform.md) | ARM64 ↔ AMD64, re-verifikasi, fuzz, checksum, PostgreSQL, gap RHEL/RKE |
| [spikes/](spikes/README.md) | prototipe buang-pakai (**BUKAN KODE PRODUKSI**) |

## Kode produksi (mulai Phase 2)

| Path | Isi |
|---|---|
| `cmd/gateway`, `internal/edge`, `internal/identity`, `internal/session`, `internal/config` | gateway: OIDC (code + PKCE, verifikasi JWKS ID token & access token), sesi server-side di Redis, CSRF/origin, rate limit, proxy `/editor/*` ke draw.io resmi, host page + adapter stub, token internal EdDSA ≤ 5 menit |
| `Dockerfile` | satu Dockerfile multi-target (`SERVICE_NAME`), `CGO_ENABLED=0`, scratch, uid 65532 |
| `Jenkinsfile` | lint, test, govulncheck, audit CGO → Kaniko → Harbor `drawio-custom-service` → bump `image.tag` **dan** `image.digest` di helm-values-lab |
| `e2e/` | E2E Chrome nyata terhadap lab (`node e2e/phase2_gateway.mjs`; baca password uji dari `lab/SECRETS.md`) |
| `scripts/dev-image.sh` | build multi-arch lokal + push ke Harbor lab lewat bastion (hanya sampai Jenkins membangun repo ini) |
| `deploy/nginx/drawio.lab.local.conf` | vhost nginx lab (TLS edge) satu-satunya jalan masuk |

Gerbang lokal (sama dengan Jenkins): `golangci-lint run ./cmd/... ./internal/...`, `go vet ./...`, `go test -race ./...`, `govulncheck ./...`, audit CGO.
Tidak diwarisi dari n8n-custom-service: `InsecureSkipVerify`, penerimaan ID token tanpa verifikasi tanda tangan.

# ADR-0012 — Kompatibilitas build dan runtime

Author / Agent: Claude Code
Timestamp: 2026-10-25 02:15
Status: Accepted (kebijakan build) · Proposed (syarat runtime RHEL/RKE, menunggu validasi di cluster kantor)

## Context
ADR-0011 menetapkan satu sumber untuk ARM64 dan AMD64. ADR ini menetapkan **bagaimana** image dibangun, dipromosikan, dan dijalankan, berdasarkan eksperimen
SPIKE-002 (Docker Desktop arm64 + buildx amd64, matriks hardening container, Trivy, promosi digest antar-registry).

## Decision
### Build
1. Dockerfile multi-stage mengikuti `n8n-custom-service/Dockerfile`: stage build `--platform=$BUILDPLATFORM` + `GOOS=$TARGETOS GOARCH=$TARGETARCH`,
   `CGO_ENABLED=0`, `-trimpath -ldflags "-s -w -buildid="`, stage akhir `scratch` + CA bundle + tzdata + `/etc/passwd` uid 65532. EVIDENCE: build satu Dockerfile
   untuk `linux/amd64,linux/arm64` berhasil; kedua binary `statically linked`, `go version -m` → `CGO_ENABLED=0` (`spikes/spike-006-crossbuild/`).
2. **Tanpa CGO, tanpa library native**: PostgreSQL = `pgx` (Go murni), YAML/JSON/XML = stdlib. Dependensi yang membutuhkan CGO (mis. SQLite native, libxml2, image processing)
   ditolak kecuali ADR baru. Diaudit di CI: `go list -deps` + `go version -m` harus menunjukkan `CGO_ENABLED=0`.
3. Build image **sekali** di CI sebagai manifest-list (arm64+amd64); tag mutable (`latest`) tidak dipakai di manifest GitOps; values memakai `image.tag` + (target) `image.digest`.
4. SBOM + provenance: gunakan `buildx --sbom=true --provenance=mode=max` bila registry mendukung; image draw.io upstream sudah membawa attestation provenance untuk kedua platform (EVIDENCE `imagetools inspect`).
5. Pemindaian: Trivy (vuln, HIGH/CRITICAL) sebagai gerbang CI untuk image custom dan sebagai pemeriksaan saat memilih tag draw.io. Hasil draw.io 32.6.1: 4 HIGH, semuanya di `usr/bin/pebble` (stdlib Go 1.27.1, komponen ACME yang tidak dipakai), set identik di arm64 dan amd64; tidak ada HIGH/CRITICAL di paket OS/Java (EVIDENCE, `spikes/spike-006-crossbuild/trivy-*.json`).
6. Reproduksibilitas: base image Go dipin digest; `go.sum` di-commit; tag draw.io dipin **digest** (Dockerfile upstream meng-clone HEAD `jgraph/drawio` saat build — INFERENCE dari `docker-drawio/main/Dockerfile` branch dev — sehingga tag bisa dibangun ulang; digest tidak).

### Registry / promosi
7. Promosi lab → Harbor kantor berdasarkan **digest manifest-list** (`skopeo copy --all --preserve-digests` / `crane copy` / `docker buildx imagetools create`). EVIDENCE: digest `sha256:3d0a5a5f…` identik setelah push ke registry:2 dan setelah salinan registry→registry (`spike-006`). Transfer tar air-gapped dan Harbor kantor = **UNVERIFIED**.
8. Image draw.io disalin ke Harbor internal sebelum dipakai (air-gapped); runtime tidak boleh menarik dari Internet.

### Runtime (securityContext & Kubernetes)
9. **draw.io** (EVIDENCE matriks hardening, `docs/spikes/SPIKE-002 §7`):
   - WAJIB: `runAsGroup: 0` (atau `supplementalGroups: [0]`/`fsGroup: 0`); UID bebas (diuji 1234). `capabilities.drop: [ALL]` dan `allowPrivilegeEscalation: false` **bekerja**.
   - **DILARANG**: `readOnlyRootFilesystem: true` dan GID ≠ 0. Entrypoint upstream menulis `js/PreConfig.js`, `PostConfig.js`, `conf/server.xml`; bila tidak bisa menulis ia **diam-diam melewati seluruh konfigurasi `DRAWIO_*`** (hanya satu baris WARNING) dan melayani di `/` dengan konfigurasi bawaan — gateway yang mem-proxy `/editor/` akan 404. Mitigasi: readiness probe yang memeriksa `GET /editor/` (bukan `/`), dan uji kontrak di CI.
   - Hanya perlu `emptyDir` untuk `/tmp`; **tanpa PVC**.
10. **Service custom**: `runAsNonRoot`, uid 65532, `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`, `/tmp` `emptyDir`. Binary statis jalan tanpa glibc → tidak peka perbedaan distribusi RHEL/Ubuntu.
11. CA internal (Keycloak, MinIO/SeaweedFS, PostgreSQL TLS) di-mount sebagai ConfigMap/Secret dan di-append ke trust store (image scratch hanya membawa CA publik); jangan `InsecureSkipVerify` (n8n-custom-service lab memakainya — tidak boleh diwarisi).
12. Sinkron waktu node (OIDC `exp/nbf`, JWT internal): gotcha lab (clock drift VM) dicatat; cek NTP di office.
13. `kubernetes.io/arch`: manifest tidak menyematkan `nodeSelector` arsitektur (multi-arch manifest-list menyelesaikan otomatis); heterogen cluster = tidak ada di lab/office saat ini.

## Consequences
- Image draw.io bukan "hardened by default" (butuh GID 0 + rootfs writable); risiko diterima dan dikompensasi NetworkPolicy + tanpa egress + tanpa data user di disk.
- Gerbang CI bertambah: audit CGO, Trivy, uji kontrak draw.io (probe `/editor/`).
- Setiap upgrade tag draw.io mengubah digest → jalankan ulang contract test adapter (ADR-0001) dan matriks hardening.

## Alternatives
- Rebuild image draw.io dari sumber sendiri (fork) — ditolak (ADR-0001).
- `readOnlyRootFilesystem` untuk draw.io via ConfigMap `PreConfig.js` + emptyDir — **UNVERIFIED**; menimpa `js/` dengan volume menyembunyikan seluruh webapp; opsi init-container-copy belum diuji.
- Distroless/alpine dengan CGO — ditolak (tanpa manfaat; scratch cukup).

## Evidence
SPIKE-002 §4–§8; `spikes/spike-006-crossbuild/`; matriks hardening `harden.out` (disalin ke SPIKE-002 §7).

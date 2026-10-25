# ADR-0011 — Arsitektur lintas-platform (lab ARM64 ↔ office AMD64)

Author / Agent: Claude Code
Timestamp: 2026-10-25 02:10
Status: Accepted (invarian domain) · Proposed (butuh validasi RHEL/RKE untuk variabel platform)

## Context
Pengembangan di lab: MacBook Pro M4, ARM64, Docker Desktop; lab cluster = RKE2 v1.33.13 di RHEL 9.8 **aarch64**, SELinux **Permissive** (EVIDENCE, `ssh` ke
kube-worker-1/kube-controlplane 2026-10-25). Target office: RHEL 9.6/9.8, **AMD64**, RKE, kemungkinan air-gapped, Harbor, GitOps (INFERENCE dari instruksi proyek; versi RKE
dan runtime container kantor = UNKNOWN). Pertanyaannya: apakah kode, kontrak operasi, logika kolaborasi, MCP, dan persistence yang sama berlaku di keduanya, dan apa yang
hanya berbeda di lapisan build/deploy.

Aturan proyek yang mengikat: sumber dan logika yang sama untuk lab dan office; perbedaan arsitektur hanya di lapisan build/runtime/deployment; **bukti ARM64 bukan bukti AMD64**.

## Decision
### Invarian domain (identik, satu sumber, tidak boleh ada cabang per arsitektur)
| Concern | Lab ARM64 | Office AMD64 | Sama? | Bukti |
|---|---|---|---|---|
| Semantic Operation Contract (ADR-0010) | JSON berversi | idem | YA | tidak ada field/jenis op bergantung arsitektur; fuzz identik di kedua arsitektur |
| Aturan merge & logika kolaborasi | Go murni | idem | YA | fuzz seed sama → 38.280 op, metrik dan pelanggaran identik arm64 vs amd64 (EVIDENCE) |
| Authz/PDP | idem | idem | YA | logika murni, tanpa syscall khusus |
| Checksum kanonik (CANON-v0) | JS + Go | JS + Go | YA | 174/174 dokumen: hash Go (linux/arm64 dan linux/amd64) = hash JS dari model hidup (EVIDENCE); browser Chrome arm64 dan Chromium x86-64 |
| MCP | HTTP + JSON | idem | YA | tidak ada dependensi native |
| Model persistence (S3 snapshot + PG op log + Redis) | idem | idem | YA | klien pgx murni Go; skema sama |
| Perilaku editor draw.io | webapp JS identik | idem | YA | tree webapp image arm64 = amd64 byte-per-byte (hash `34094c8…`); suite fungsional identik di Chrome Mac dan Chromium x86-64 Linux |

### Variabel platform (hanya build/runtime/deploy)
| Concern | Lab | Office | Sama? |
|---|---|---|---|
| Arsitektur image | linux/arm64 | linux/amd64 | TIDAK |
| Node/Kubernetes | RKE2 di RHEL 9.8 aarch64, SELinux Permissive | RKE (varian?) di RHEL 9.6/9.8 amd64, SELinux = UNKNOWN | TIDAK |
| Registry | Harbor lab (`harbor.lab.local`) | Harbor kantor, kemungkinan air-gapped | SEBAGIAN (protokol sama) |
| Ingress/TLS | ingress-nginx + VM nginx edge | ingress kantor | TIDAK (nilai values) |
| Identity provider/endpoint | Keycloak realm `lab` | Keycloak kantor | TIDAK (konfigurasi) |
| Sumber image draw.io | `docker.io/jgraph/drawio` (egress lab) | mirror internal (air-gapped) | TIDAK (alamat), **sama (digest)** |

### Aturan
1. **Satu kode, satu Dockerfile, build multi-platform** (`docker buildx build --platform linux/amd64,linux/arm64`); semua binary Go `CGO_ENABLED=0`, statis, scratch (ADR-0012).
2. **Dilarang** build tag, branch kode, atau konfigurasi logika per arsitektur (`GOARCH`-specific file, `runtime.GOARCH` di logika). Pengecualian hanya di Dockerfile/CI.
3. **Image dipromosikan berdasarkan digest manifest-list**, bukan tag; lab dan office memakai keluarga digest yang sama (EVIDENCE: digest `3d0a5a5f…` tidak berubah setelah push dan salin registry→registry di registry:2 lokal).
4. Semua klaim "berjalan di office" dicatat sebagai **UNVERIFIED sampai diuji di RHEL/RKE AMD64 sungguhan** (daftar uji wajib di SPIKE-002 §13).
5. Perbedaan perilaku platform yang ditemukan di office tidak boleh dijawab dengan cabang logika; hanya dengan konfigurasi/deploy atau ADR baru.

## Consequences
- Lab tidak bisa membuktikan: SELinux enforcing, runtime container kantor (containerd vs Docker pada RKE1), NetworkPolicy/CNI kantor, Harbor kantor + air-gapped import, kinerja pada CPU x86 server.
- Validasi AMD64 di Mac hanya via emulasi (Docker Desktop/Rosetta atau QEMU): sah untuk **kebenaran fungsional**, **tidak sah untuk kinerja**.
- Pengembangan di ARM64 tetap valid selama CI office membangun ulang dari sumber yang sama dan menjalankan suite kontrak yang sama (SPIKE-001/002).

## Alternatives
- Mengembangkan di VM AMD64 (UTM/QEMU) — ditolak sebagai lingkungan utama (lambat; hanya untuk validasi silang).
- Membangun image hanya amd64 dan menjalankan lewat emulasi di Mac — ditolak (mengaburkan masalah ARM native; lab cluster aarch64).
- Cabang logika per arsitektur — ditolak (melanggar aturan proyek).

## Evidence
`docs/spikes/SPIKE-002-cross-platform.md` (§3–§10); `spikes/spike-006-crossbuild/`, `spikes/spike-004-fuzz/results-xarch/`, `spikes/spike-003-checksum/`.

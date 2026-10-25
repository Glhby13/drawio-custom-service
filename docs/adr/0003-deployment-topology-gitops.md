# ADR-0003 — Topologi deploy GitOps

Author / Agent: Claude Code
Timestamp: 2026-10-25 00:05
Status: Accepted (keputusan topologi final dari user, 2026-10-24)

## Context
Lab mengikuti pola kantor: chart di `helm-base-lab`, values di `helm-values-lab/lab-manifest`, ApplicationSet di
`argocd-gitops-lab`, CI Jenkins (kaniko → Harbor → bump tag values) → ArgoCD auto-sync. Insiden 2026-10-23 (`releaseName`
di template ApplicationSet bersama) menunjukkan template bersama tidak boleh diubah demi satu service.

## Decision
**draw.io asli**
- Chart `helm-base-lab/drawio` (chart minimal sendiri: Deployment, Service, ServiceAccount, Ingress opsional).
- Values `helm-values-lab/lab-manifest/drawio/values-common.yaml`, `values-env-dev.yaml`, `values-cluster-dev-lab.yaml`.
- Satu element `- service: drawio` di `argocd-gitops-lab/argocd-applications/projects/lab-projects/dev/lab-projects-dev-lab-services.yaml`.
  **Template ApplicationSet tidak diubah** → Application `lab-drawio-dev-lab`, namespace `lab-dev`.
- Service ClusterIP saja; tanpa ingress publik (pintu masuk = gateway).

**drawio-custom-service**
- Monorepo `Glhby13/drawio-custom-service` (pola `n8n-custom-service`: satu Dockerfile multi-target `SERVICE_NAME`, Jenkinsfile
  lint/test/govulncheck → kaniko → Harbor project `drawio-custom-service` → bump `image.tag`).
- Chart generik `helm-base-lab/drawio-custom-service`; values `helm-values-lab/lab-manifest/drawio-custom-service/<svc>/` (3 file per service)
  + `common-k8s-resources`.
- AppProject + ApplicationSet terpisah `lab-drawio-custom-projects` (tiru `lab-n8n-custom-projects`).
- Gateway = satu-satunya Ingress untuk `drawio.lab.local`; vhost VM nginx (TLS edge) menunjuk ingress-nginx; websocket diaktifkan.

## Consequences
- Menambah draw.io tidak menyentuh service lain di ApplicationSet `lab-dev-lab-services` (blast radius: satu Application baru).
- Daftar deployable custom bisa berubah (ADR-0005) cukup dengan menambah/menghapus element di ApplicationSet milik proyek ini.
- Push ke repo GitOps = deploy; setiap push butuh persetujuan user dan pemantauan build/sync (aturan lab).

## Alternatives
- Semua service custom di `lab-projects-dev-lab-services.yaml` — ditolak user (path chart = nama service, butuh chart per service).
- Sidecar oauth2-proxy di depan draw.io — dibatalkan (pintu masuk = gateway custom, ADR-0004).

## Evidence
Pola yang sudah berjalan: `argocd-gitops-lab/.../lab-projects-dev-lab-services.yaml`, `.../lab-n8n-custom-projects/dev/lab-n8n-custom-projects-dev-lab-services.yaml`,
`helm-base-lab/n8n-custom-service`, `n8n-custom-service/Jenkinsfile` (dibaca 2026-10-24).

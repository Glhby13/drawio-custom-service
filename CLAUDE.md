# drawio-custom-service — konteks cepat

Author / Agent: Claude Code · Timestamp: 2026-10-25 10:00

- Tujuan: draw.io Community resmi **tanpa modifikasi** + lapisan enterprise (SSO, penyimpanan, izin, kolaborasi, MCP, AI) di `drawio-custom-service`.
- Sumber kebenaran: `docs/MASTER-PLAN.md` (bagian "Status Phase 1" memuat kebijakan image + digest), `docs/adr/0001…0012`, `docs/spikes/`. Tracking lab vs kantor: `../docs/office-sync/DRAWIO-CUSTOM-SERVICE-OFFICE-VS-LAB.md`; langkah kantor Phase 1: `../docs/office-sync/DRAWIO-PHASE1-OFFICE-STEPS.md`.
- Status (2026-10-25): **Phase 0–8 selesai di lab** (gateway, platform, mcp-server live via GitOps; E2E 25+44+43 lulus). Model kolaborasi ADR-0006 = Accepted untuk lab. Kantor RHEL/RKE = **UNVERIFIED** (langkah: `../docs/office-sync/DRAWIO-PHASE2-8-OFFICE-STEPS.md`). Sisa: mode saran AI, skala 500+ sel, auth-code+PKCE dari agen sungguhan, kantor.
- Guardrail: jangan fork/patch draw.io; jangan ubah ADR demi kemudahan; jangan ubah template ApplicationSet bersama; `InsecureSkipVerify` dilarang; tidak ada `git push` tanpa persetujuan user.
- draw.io runtime: `runAsGroup:0`, rootfs writable, probe `/editor/` (bila GID≠0 / rootfs read-only, `DRAWIO_*` dilewati senyap). Image dipin digest index; upgrade = ulangi hardening + Trivy + smoke.
- `spikes/` = kode buang-pakai, bukan produksi. Push ke GitHub memicu Jenkins (multibranch) → Harbor → bump tag+digest di helm-values-lab → ArgoCD; mcp-server/platform/gateway = 3 service di `SERVICES` (Jenkinsfile) dan ApplicationSet `lab-dev-drawio-custom-services`.
- Manifest ada di repo lain: `helm-base-lab/drawio`, `helm-values-lab/lab-manifest/drawio/`, `argocd-gitops-lab/.../lab-projects-dev-lab-services.yaml`.
- Memori bersama: agentmemory project `lab`. Rules: `../rules.md` (kantor read-only, push butuh persetujuan).

- Dev loop: `LOCAL_WEB=1 node e2e/phase5_collab.mjs` memakai aset `internal/edge/web/` dari disk (aset dibenamkan di image gateway; tanpa flag = versi ter-deploy). Jenkins: Go checks dijalankan berurutan (tiga build Go paralel di satu container → OOM cgroup di node lab kecil → build `ABORTED` "Agent was removed").

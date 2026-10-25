# drawio-custom-service — konteks cepat

Author / Agent: Claude Code · Timestamp: 2026-10-25 10:00

- Tujuan: draw.io Community resmi **tanpa modifikasi** + lapisan enterprise (SSO, penyimpanan, izin, kolaborasi, MCP, AI) di `drawio-custom-service`.
- Sumber kebenaran: `docs/MASTER-PLAN.md` (bagian "Status Phase 1" memuat kebijakan image + digest), `docs/adr/0001…0012`, `docs/spikes/`. Tracking lab vs kantor: `../docs/office-sync/DRAWIO-CUSTOM-SERVICE-OFFICE-VS-LAB.md`; langkah kantor Phase 1: `../docs/office-sync/DRAWIO-PHASE1-OFFICE-STEPS.md`.
- Status: Phase 0 selesai; Phase 1 = **PASS WITH CONDITIONS** (live di lab, Chrome asli PASS; kantor UNVERIFIED). **Phase 2+ terkunci** (smoke Phase 1 di lab dan kantor + ADR-0004 token). **Phase 5 terkunci** sampai SPIKE-003. Model kolaborasi = HYPOTHESIS; kantor RHEL/RKE = UNVERIFIED.
- Guardrail: jangan fork/patch draw.io; jangan ubah ADR demi kemudahan; jangan implementasi kolaborasi/MCP/AI sebelum fasenya dibuka; jangan ubah template ApplicationSet bersama; `InsecureSkipVerify` dilarang; tidak ada `git push` tanpa persetujuan user.
- draw.io runtime: `runAsGroup:0`, rootfs writable, probe `/editor/` (bila GID≠0 / rootfs read-only, `DRAWIO_*` dilewati senyap). Image dipin digest index; upgrade = ulangi hardening + Trivy + smoke.
- Repo ini belum punya commit; `spikes/` = kode buang-pakai, bukan produksi.
- Manifest ada di repo lain: `helm-base-lab/drawio`, `helm-values-lab/lab-manifest/drawio/`, `argocd-gitops-lab/.../lab-projects-dev-lab-services.yaml`.
- Memori bersama: agentmemory project `lab`. Rules: `../rules.md` (kantor read-only, push butuh persetujuan).

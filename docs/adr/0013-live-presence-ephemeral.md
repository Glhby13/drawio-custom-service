# ADR-0013 — Live presence adalah state ephemeral terpisah dari document state

Author / Agent: Claude Code
Timestamp: 2026-10-25 17:05
Status: **Accepted untuk lab (2026-10-25)** — bukti: `docs/spikes/SPIKE-004-live-presence.md`, `e2e/phase7_presence.mjs`

## Context
Kolaborasi saat ini menampilkan perubahan setelah commit. Pengguna ingin melihat aktivitas yang sedang berlangsung (memilih, mengedit, menarik) tanpa merusak model durable (ADR-0006/0007/0010).

## Decision
1. **Dua jalur terpisah.** Document state = operasi semantik → op log PostgreSQL → riwayat/versi/audit. Presence = pesan `act` ephemeral lewat WebSocket + Redis pub/sub + memori hub; tanpa DB, tanpa op log, tanpa audit, tanpa MCP.
2. **Presence tidak pernah lock dan tidak pernah sumber otorisasi.** Konflik tetap diselesaikan layer operasi semantik; presence hanya memberi tahu.
3. Identitas pengirim diisi server dari token; peran membatasi state yang boleh dilaporkan (viewer/commenter: `selecting` saja); akses dokumen = syarat membuka kanal.
4. Semua entri punya TTL + heartbeat; best effort, rate-limited, di-drop bila berlebih.
5. Deteksi sisi browser hanya memakai API draw.io yang terdokumentasi/teramati (selection model, editing events, graph/vertex handler, overlay pane); `mxEvent.CHANGE` model bukan batas presence; draw.io tidak di-patch.
6. AI muncul sebagai kolaborator dengan state jujur `applying` (server), bukan simulasi mengetik.

## Consequences
+ Tidak ada perubahan skema, kontrak operasi, MCP, atau authz AI. + Latensi rendah, tanpa tulis DB per gerakan mouse.
− Presence hilang saat restart pod (sengaja); draft teks terlihat oleh semua yang berhak membaca dokumen (tidak ada ACL per sel di model ini); caret langsung tidak ada (P3).
## Alternatives
Presence sebagai operasi (ditolak: membanjiri op log/riwayat); lock saat edit (ditolak: menghalangi kolaborasi, tidak dibutuhkan); broker/stream baru (ditolak: transport yang ada cukup).

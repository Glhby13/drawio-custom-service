# AI Assistant (integrated-ai-service) — rencana dan konteks

Author / Agent: Claude Code
Timestamp: 2026-11-10 15:10 WIB
Status: **DIRENCANAKAN, belum ada kode, belum ada ADR.** Dokumen ini hanya mengingat keputusan supaya sesi/agent berikutnya langsung paham. Ditulis dari diskusi user ↔ Claude Code 2026-11-10. Saat pengembangan dimulai: tulis ADR-0023 (nomor 0022 dipakai 2026-11-10 untuk akses menu AI/MCP lewat grup Keycloak, di salinan kantor `draw-space`; AI Assistant sebaiknya memakai mekanisme role yang sama dengan role terpisah, mis. `ai-assistant-access`) dan perbarui dokumen ini.

## 1. Tujuan

Chat AI terintegrasi di dalam DrawSpace. Nama di UI: **AI Assistant**. Model: Qwen 35B (A3B) yang dilayani lewat **LiteLLM** (sudah ada di klaster kantor). Pemakai: orang yang punya akses ke sebuah diagram.

- Chat **terpisah per diagram** (thread kunci `document_id`, tanpa memori lintas diagram).
- **Selalu terlihat siapa yang menyuruh AI** saat AI membuat/mengubah diagram.
- Service sendiri: `integrated-ai-service` (service ke-4 di repo `drawio-custom-service`, sesudah gateway, platform, mcp-server).
- **Berbeda dari fitur AI/MCP** (menu AI → MCP access, token `dsp_…`, ADR-0008/0009/0016). Menu MCP itu sedang disembunyikan di kantor sejak 2026-11-10; AI Assistant punya menu, flag, dan kebijakan sendiri dan tidak terpengaruh.

## 2. Keputusan user (2026-11-10)

| Topik | Keputusan |
|---|---|
| Lokasi model | LiteLLM di klaster yang sama dengan DrawSpace, lewat koneksi internal klaster |
| Konfigurasi | Endpoint, model, dan kredensial (token, atau user+password) diatur dari **manifest**, mudah diganti |
| Lingkungan | **Langsung kantor**. Lab tidak kuat menjalankan LiteLLM |
| Pengirim prompt | **Editor (dan owner) boleh mengubah**. Viewer dan commenter hanya bertanya (mode baca). Commenter bukan fokus: role itu tidak dipakai dan tidak dikembangkan |
| Mode perubahan | Prompt bertanya/minta saran → AI memberi **saran/usulan yang menunggu Accept**. Prompt perintah membuat/mengubah → AI **langsung mengubah** |

## 3. Fakta lingkungan kantor (dibaca dari manifest, 2026-11-10)

- LiteLLM: namespace `ai-dev`, service `litellm` port 4000, image `ghcr.io/berriai/litellm:v1.106.2`. URL klaster: `http://litellm.ai-dev.svc.cluster.local:4000` (OpenAI-compatible: `/v1/chat/completions`, `/v1/models`). Manifest lama: `app-of-apps/project-manifest/ai-projects/litellm`. Secret `litellm-env` (key master/UI/DB: nilainya di Secret, tidak dicatat di sini).
- Model dan alias disimpan **di database LiteLLM** (`STORE_MODEL_IN_DB`), bukan di git. Alias Qwen 35B harus dilihat dari UI LiteLLM atau `GET /v1/models`. **Belum diketahui.**
- DrawSpace: namespace `platform-dev` (dipakai bersama aplikasi lain; semua resource baru wajib bernama `drawio-*`). Values: `app-of-apps/helm-values/platform-manifest/drawio-custom-service/<service>/{values-common,values-env-dev,values-cluster-dev-onprem-dev-1}.yaml`. Chart satu untuk semua: `helm-base/drawio-custom-service`. Daftar service: elemen list di ApplicationSet `drawio-custom-dev-onprem-dev-1-services` (`argocd-gitops/.../drawio-custom-projects`). **Menambah service = satu baris `- service: <nama>`; template ApplicationSet DILARANG diubah** (insiden `releaseName` 2026-10-23).
- Secret dibuat **manual** (bukan di git): `drawio-gateway-secret`, `drawio-platform-secret`, `drawio-mcp-secret`; service baru ikut pola ini (`drawio-ai-secret`).
- Gateway = satu-satunya pintu browser (ADR-0003/0004); service lain tanpa ingress publik kecuali mcp-server.
- ⚠ **NetworkPolicy**: `app-of-apps/helm-values/platform-manifest/network-isolation/cluster-dev-onprem-dev-1.yaml` mendefinisikan `ai-dev` hanya menerima dari `ai-dev`, `aiko-dev`, `aiko-uat`, dan `platform-dev` dengan `allowedNamespaces: []`; template (`helm-base/network-isolation`) juga membatasi egress ke namespace yang sama + DNS. **Belum diverifikasi apakah live** (gateway berhasil menjangkau Redis/SMTP di luar klaster, jadi mungkin tidak berlaku di `platform-dev`). Cek: `kubectl get networkpolicy -A`. Bila live, `ai-assistant` di `platform-dev` tidak bisa memanggil LiteLLM. Perbaikan = mengubah file bersama (blast radius banyak namespace) atau lewat ingress: **butuh konfirmasi user**.
- Catatan: cek konektivitas awal memakai domain salah (`litellm.dev.btn.do.id`); itu **bukan** bukti LiteLLM tak terjangkau.
- ThinkStation (`172.16.130.18`, vLLM Qwen3.6-35B-A3B, dimiliki `moodjair`) adalah alternatif endpoint langsung; dipakai bersama layanan lain, cocok untuk pilot saja.

## 4. Arsitektur yang diusulkan

```
Browser (panel "AI Assistant" per diagram)
   │ route /ai/*  (edge authn, ADR-0004)
Gateway ──► integrated-ai-service ──► LiteLLM (ai-dev) ──► model Qwen 35B
              │  tabel ai_* (DDL 006), kunci Redis per diagram
              ├─ otorisasi: tanya platform (PDP) "user X boleh read/edit diagram D?"
              └─ tool call ─► lapisan MCP yang sudah ada (build_diagram/lint/layout, ADR-0017)
                              ─► platform ─► op log ─► broadcast (tanpa jalur belakang ke DB/S3)
```

- **Service Go terpisah**, tanpa ingress sendiri; gateway mem-proxy `/ai/*` (env `AI_UPSTREAM`). Tidak memegang kredensial dokumen/S3. Tabel `ai_*` miliknya sendiri; izin dokumen selalu ditanyakan ke platform. Perlu cek `internal/mcpsrv` apakah jalur internal bertanda tangan ke tool MCP sudah cukup atau perlu endpoint kecil baru.
- **Terpisah antar diagram**: konteks LLM hanya dari thread + diagram itu; tool dikunci ke satu diagram (pola `docScope`, ADR-0016).
- **Atribusi (beda dari ADR-0016)**: aktor `ai-assistant`, `onBehalfOf` = **user pengirim prompt** (bukan pemilik). Izin efektif = min(izin user, kebijakan AI diagram) per ADR-0009. `turn_id` dipakai sebagai `batchId` operasi → riwayat menampilkan "AI atas nama Budi: *<prompt>*" dan revert per permintaan memakai mekanisme batch yang ada. Usulan yang di-Accept mencatat "diminta A, disetujui B".
- **Mode per giliran**: maksud pesan dinilai (structured output): `ask` → jawaban teks + usulan (daftar operasi ADR-0010 disimpan, belum diterapkan, menunggu Accept); `do` → diterapkan langsung. Ragu = usulan (default aman). Viewer/commenter dipaksa `ask` tanpa tool tulis, tanpa tombol Accept (ditegakkan server). Usulan basi (diagram berubah) ditandai kedaluwarsa.
- **Satu turn aktif per diagram** (kunci Redis) + antrean ("AI sedang mengerjakan permintaan Budi"); jawaban di-stream ke semua peserta lewat kanal kolaborasi (ADR-0013), pesan akhir disimpan.
- **Keamanan**: isi diagram = data tidak tepercaya (prompt injection lewat label sel); tanpa tool web/jaringan; batas ops/menit dan `maxDelete` yang ada tetap berlaku; saklar mati global + per diagram; konten tetap on-prem.

## 5. Konfigurasi dari manifest

| Setting | Tempat | Catatan |
|---|---|---|
| `LLM_BASE_URL` | `values-env-dev.yaml` | default `http://litellm.ai-dev.svc.cluster.local:4000/v1`; bisa diganti ke vLLM langsung |
| `LLM_MODEL` | `values-env-dev.yaml` | alias model di LiteLLM (belum diketahui) |
| `LLM_AUTH` | `values-env-dev.yaml` | `bearer` (default) / `basic` / `header:<nama>` |
| `LLM_API_KEY` atau `LLM_USERNAME` + `LLM_PASSWORD` | Secret `drawio-ai-secret` (manual) | jangan master key; pakai **virtual key** LiteLLM khusus DrawSpace dengan batas model, rpm/tpm, budget |
| timeout, konkurensi, CA kustom | `values-env-dev.yaml` | `InsecureSkipVerify` dilarang |

## 6. Fase (langsung kantor)

| Fase | Isi | Gerbang keluar |
|---|---|---|
| P0 verifikasi | `kubectl get netpol -A`; `GET /v1/models` ke LiteLLM dari pod `platform-dev`; catat alias Qwen; buat virtual key; cek spend logs menyimpan prompt atau tidak; uji 10 prompt diagram dengan tool MCP yang ada (kualitas tool-calling, latensi, konteks) | jaringan terbukti; bila tool-calling Qwen kurang andal → satu panggilan output terstruktur ke `build_diagram` |
| P1 | Skeleton service, klien LLM, tabel thread (DDL 006), otorisasi, route gateway, panel chat, tanya jawab saja | thread terpisah antar diagram terbukti tes |
| P2 | Tool tulis, atribusi `onBehalfOf` + `turn_id`, usulan + Accept, revert per permintaan | riwayat menunjukkan penyuruh |
| P3 | Thread bersama, streaming, antrean, presence AI | E2E dua user satu diagram |
| P4 | Tes penyalahgunaan (injection, lintas diagram, viewer), kuota, audit, saklar mati | seperti `run_invite_abuse` |
| P5 | Register `- service: …` di ApplicationSet drawio, values, Secret, DDL kantor **dulu** baru image; Jenkinsfile `SERVICES`; dokumen langkah kantor + ledger | push hanya setelah user setuju |

Tes otomatis di lab memakai **stub LLM** kecil kompatibel OpenAI (lab tidak menjalankan LiteLLM); integrasi nyata baru terbukti di kantor.

## 7. Pertanyaan terbuka (default usulan bila user "ikut saran")

1. Jaringan `ai-dev` ← `platform-dev`: bila policy live, (a) tambah `platform-dev` ke `ai-dev` di `network-isolation` atau (b) lewat ingress `litellm.dev.btn.co.id`. Usul: tunggu hasil P0.
2. Siapa boleh Accept: semua editor (usul), keduanya tercatat.
3. Siapa melihat riwayat chat: semua yang punya akses diagram, termasuk viewer (usul).
4. Retensi chat: ikut diagram (hilang saat hapus permanen, ikut Trash/Restore ADR-0018) (usul).
5. Spend logs LiteLLM menyimpan isi prompt? Bila ya, matikan/batasi.
6. Kuota: usul 1 turn aktif per diagram, 20 permintaan per user per jam; estimasi pengguna bersamaan belum diketahui.
7. Diagram besar (500+ sel): AI diberi ringkasan + halaman per halaman (usul).
8. Alur kode: sumber di lab (`drawio-custom-service`), disalin ke `/Users/macbook/Documents/custom-apps/draw-space` (salinan kantor; path lama `Documents/draw-space` sudah pindah) untuk push kantor (seperti fitur sebelumnya), atau office-first? **Belum diputuskan.**
9. Pratinjau usulan: teks + sorot sel terdampak di editor (usul); pratinjau gambar menyusul (draw.io tanpa modifikasi, ADR-0001).
10. DDL: nomor 006 sementara (periksa `SchemaVersion` tertinggi saat mulai).

## 8. Aturan saat pengembangan dimulai

- Ikuti definisi selesai: README, `CLAUDE.md`/`GEMINI.md` drawio, ledger `lab/docs/office-sync/PENDING-LAB-CHANGES.md`, dokumen langkah kantor `docs/office-sync/DRAWIO-AI-ASSISTANT-OFFICE-STEPS.md` (DDL, Secret, values, ApplicationSet, Jenkins), `memory_save`.
- DDL kantor: berikan perintah siap-salin ke user sebelum deploy (aturan global).
- Jangan ubah template ApplicationSet bersama atau `network-isolation` tanpa menjelaskan dampak dan meminta konfirmasi user.
- Tidak ada `git push` tanpa persetujuan user; pantau build Jenkins dari console.

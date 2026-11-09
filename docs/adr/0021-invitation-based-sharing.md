# ADR-0021 — Berbagi lewat undangan yang harus diterima penerima

Author / Agent: Claude Code (template email + paket mailer: Antigravity sonnet-5-5-medium, diverifikasi Claude)
Timestamp: 2026-11-09
Status: **Accepted (2026-11-09)** — merevisi cara berbagi pada ADR-0004 (PDP) dan ADR-0019 (chip Shared); melengkapi ADR-0016 (token MCP).

## Context
Berbagi lama langsung memberi akses: `PUT /documents/{id}/permissions` menulis baris `permissions`, dan diagram seketika muncul di daftar penerima. Pemilik yang salah memilih orang, atau iseng, bisa memenuhi daftar diagram orang lain. Share "everyone" (`*`) memperburuknya: diagram muncul di daftar **semua** pengguna. Role `commenter` juga tidak lagi dipakai.

## Decision
1. **Akses baru hanya lahir setelah penerima menerima.** Pemilik membuat *undangan*; baris `permissions` baru ditulis saat penerima menekan **Terima** di `/invite/{token}`.
2. **Tiga jenis undangan** (tabel `invitations`, migrasi `005_invitations.sql`, `schema_version` 5):
   - **A. user + email**: terikat ke satu akun; gateway mengirim email (pengirim `DrawSpace <noreply@…>`, subjek `Undangan akses diagram '<judul>'`, tanpa Reply-To, teks Indonesia).
   - **B. user tanpa email**: terikat ke satu akun; pemilik menyalin tautannya (alamat email tidak terdeteksi: tombol kirim email nonaktif).
   - **C. invitation link**: siapa saja yang login boleh menerima; multi-pakai sampai kedaluwarsa/dicabut.
   Role undangan hanya **viewer** atau **editor**. Role *commenter* disembunyikan penuh (UI, API, token MCP); baris lama dilipat menjadi viewer oleh migrasi.
3. **Masa berlaku bisa diatur untuk semua jenis**: 1/7/30/90/365 hari, Never, atau Custom 1..365 hari (`expiresInDays`, 0 = never, default **7**), mengikuti pola token MCP (ADR-0016). Divalidasi di server.
4. **"Share with everyone" (`*`) dihapus.** `PUT permissions` kini hanya mengubah role atau mencabut orang yang SUDAH punya akses (viewer/editor); menambah orang baru ditolak `400 use_invitation`; subject `*` dan role `commenter` ditolak. Pengecualian: aktor AI (`users.kind='ai'`) tetap dibagikan langsung (bukan orang yang bisa dispam, dan tidak bisa menerima).
5. **Pemilik tetap bisa mengubah role dan mencabut** akses siapa pun yang ia beri.
6. **Batas kirim email**: satu email per (pengirim, penerima, diagram) per hari, reset 00.00 WIB. Dihitung dari `invitations.email_sent_at`, **diambil saat undangan dibuat/dikirim ulang** di dalam transaksi yang dikunci per (diagram, penerima) (`pg_advisory_xact_lock`): 20 permintaan paralel menghasilkan tepat satu undangan dan satu email (diuji `TestIT_Invitations`). Undangan yang dibatalkan/disembunyikan tetap dihitung (`hidden_at`, bukan DELETE) sehingga batal-lalu-buat-baru tidak melewati batas. Bila gateway gagal mengirim, jatah dikembalikan lewat endpoint internal `email-failed`. Link (B/C) tidak dibatasi.
7. **Pembagian tugas gateway vs platform** (ADR-0004): platform memegang aturan undangan, token, dan batas harian; gateway yang mencari email di Keycloak (platform tidak punya rute ke Keycloak/SMTP; `users.email` hampir selalu kosong untuk SSO karena token internal tidak membawa email) dan mengirim email; platform sudah mengambil jatah harian saat membuat undangan, dan gateway hanya memanggil endpoint internal (token layanan) `email-failed` bila pengiriman gagal.

## Security
- Token 256-bit acak (`dsi_…`); hanya SHA-256 yang disimpan; dibandingkan lewat lookup hash; token ditampilkan sekali kepada pemilik (kecuali bila email terkirim: token tidak pernah sampai ke browser pemilik).
- Undangan jenis user dicocokkan dengan username penerima; akun lain menerima `403 invitation_not_for_you` tanpa detail diagram. Token tidak dikenal/kedaluwarsa/dicabut/terpakai = satu jawaban `404 invitation_invalid` (tanpa oracle).
- Rate limit per orang pada preview/accept/decline (anti tebak token); batas 20 link aktif dan 50 undangan pending per diagram; aktor AI dan token MCP ditolak di semua endpoint undangan.
- Halaman `/invite/{token}`: login dulu (`return_to`), `Referrer-Policy: no-referrer`, `Cache-Control: no-store`. Semua teks pengguna di-escape (html/template); subjek email dibersihkan dari karakter kontrol; alamat penerima divalidasi (satu alamat polos, tanpa CR/LF).
- Terima tidak pernah menurunkan role yang sudah lebih tinggi; pemilik tidak bisa menerima undangan diagramnya sendiri.
- **Hasil tinjauan keamanan independen** (gemini-4-argon-high, 2026-11-09; 8 temuan, semuanya ditindaklanjuti):
  - katup email lintas diagram di atas "satu email per diagram": maks **10 email/hari** dari satu pengirim ke satu penerima (semua diagram) dan **100/hari** per pengirim (`email_quota`), dihitung di bawah kunci pengirim (urutan kunci: pengirim, lalu diagram) supaya permintaan paralel tidak melewatinya;
  - gateway **memeriksa hak berbagi di platform sebelum mencari alamat email** (tanpa oracle "punya email atau tidak" bagi orang yang bukan pemilik) dan membatasi pencarian per orang (burst 10, 1 per 2 detik);
  - gateway menolak (400) badan JSON yang parser-nya tolak, dan `readJSON` platform kini mewajibkan tepat satu nilai JSON (tanpa selisih parser yang membuat gateway terlewati);
  - `resend` hanya untuk undangan yang masih berlaku dan penerimanya belum punya akses (undangan kedaluwarsa tidak bisa dihidupkan lagi, tidak menembus batas 50), dan jatah email dikembalikan juga saat tidak ada email atau permintaan browser terputus (`context.WithoutCancel`);
  - kunci per diagram untuk seluruh undangan user (batas 50 tidak bisa dilewati lewat penerima berbeda);
  - perbandingan username penerima tidak membedakan huruf besar/kecil (tidak pernah lebih longgar);
  - panggilan API yang mengubah data (POST/PUT/PATCH/DELETE) kini **wajib membawa token CSRF sesi** selain pemeriksaan Origin + SameSite (hanya skrip situs sendiri yang bisa membacanya lewat `/api/me`).

## Data lama (migrasi 005, non-destruktif)
Baris `permissions` subject `*` dan role `commenter` (juga `document_tokens.role='commenter'`) **disalin ke `access_archive`** sebelum dihapus/dilipat ke viewer. Tidak ada dokumen, versi, op, snapshot, atau komentar yang disentuh. Pemulihan: `INSERT INTO permissions … SELECT … FROM access_archive WHERE reason='everyone'` (lihat langkah kantor).

## Consequences
- Berbagi menjadi dua langkah (undang, terima); daftar penerima tidak lagi bisa "dipenuhi" oleh pihak lain.
- Gateway kini butuh SMTP (`SMTP_*`, `PUBLIC_URL`); tanpa SMTP undangan tetap bisa lewat tautan.
- Skema wajib v5 sebelum image baru berjalan (platform menolak start bila kurang).

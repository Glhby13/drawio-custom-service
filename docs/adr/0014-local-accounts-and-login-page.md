# ADR-0014 — Own login page and local accounts next to SSO

Author / Agent: Claude Code
Timestamp: 2026-10-25 19:00
Status: **Accepted untuk lab (2026-10-25)** — bukti: `e2e/phase9_local.mjs`, `TestLocalAccounts` (PostgreSQL lab), `internal/edge` tests
Amends: ADR-0004 (edge authn).

## Context
Sampai sekarang satu-satunya cara masuk adalah SSO Keycloak. Dibutuhkan halaman login sendiri (DrawSpace) dan akun lokal (admin dan user biasa) yang bisa berkolaborasi dengan pengguna SSO, juga sebagai jalur darurat bila IdP mati.

## Decision
1. **Halaman `/login`** (publik, CSP ketat tanpa `unsafe-inline`, stylesheet di-hash): form username/password + tombol "Sign in with SSO" (`/login/sso` = alur OIDC lama). Ini tujuan redirect untuk request tanpa sesi.
2. **Akun lokal** disimpan di PostgreSQL platform (`local_users`, DDL 002, `schema_version` 2): hash **argon2id** (m=19 MiB, t=2, p=1; parameter ikut tersimpan), peran `user|admin`, `disabled`. Admin pertama dibootstrap dari Secret (`LOCAL_ADMIN_PASSWORD`) hanya bila belum ada admin lokal.
3. **Verifikasi kredensial di platform**, bukan di gateway: gateway meneruskan ke `POST /api/v1/internal/auth/local` dengan token layanan 30 dtk (`sub=gateway-auth`, `actorType=service`, issuer gateway); endpoint itu 404 untuk browser (gateway memblokir `/api/v1/internal/*`) dan 403 untuk token lain (termasuk edge MCP).
4. **Sesi lokal** = sesi Redis yang sama (Idp `local`, tanpa ID token → logout tidak memanggil Keycloak). Token internal ke platform membawa `idp=local`; **platform menentukan peran dan status dari database** (cache 15 dtk): akun yang dinonaktifkan/dihapus terputus walau sesinya masih hidup; token `idp=local` dari issuer selain gateway ditolak.
5. **Satu ruang nama pengguna**: akun lokal tidak boleh memakai username principal SSO yang sudah dikenal (409), dan principal non-lokal yang username-nya sama dengan akun lokal ditolak (403) — mencegah pewarisan diagram/share lewat nama yang sama.
6. **Perlindungan brute force**: 5 gagal per username / 20 per alamat dalam 15 menit → 429 (`Retry-After`), hitungan di Redis; "unknown user" memakai hash dummy (waktu sama); pesan generik.
7. Admin (lokal atau SSO) mengelola akun dari UI **Users** (`/admin/users`): buat, ubah peran/nama/email, reset password, nonaktifkan, hapus (ditolak bila masih memiliki diagram; share dihapus bersama akun). Tidak boleh menonaktifkan/menurunkan diri sendiri atau admin lokal terakhir. Pengguna lokal mengganti password sendiri dari menu pengguna.
8. `/readyz` tidak lagi bergantung pada IdP (hanya Redis): sign-in lokal tetap hidup saat SSO mati.

## Consequences
+ Jalur masuk darurat dan pengguna tanpa SSO. + Kolaborasi lokal↔SSO lewat daftar share yang sama (picker mencari keduanya).
− DDL 002 wajib di PostgreSQL kantor sebelum image baru (service menolak start bila `schema_version` < 2). − Password lokal = tanggung jawab kita (kebijakan min 10 karakter; belum ada MFA untuk akun lokal, reset oleh admin). − Akun lokal tidak ikut flow OTP Keycloak.
## Alternatives
Akun lokal di Keycloak (ditolak: butuh admin Keycloak dan tidak jadi jalur darurat saat IdP mati); hash di gateway (ditolak: gateway tanpa akses DB, bertambah permukaan).

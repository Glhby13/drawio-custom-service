# drawio-custom-service

Author / Agent: Claude Code
Timestamp: 2026-10-25 03:15

Lapisan enterprise di sekitar **draw.io Community** (image `jgraph/drawio` dipakai apa adanya): SSO Keycloak, penyimpanan diagram terpusat
(SeaweedFS S3 + PostgreSQL), izin/share, versi, kolaborasi, audit, MCP, dan AI sebagai kolaborator — tanpa mengubah draw.io dan tanpa PVC.
Pola repo dan deploy mengikuti `n8n-custom-service`. Satu sumber untuk lab (ARM64) dan office (AMD64).

**Status (2026-10-25, lab-first): Phase 0–8 selesai di lab — SSO, penyimpanan terpusat (PostgreSQL + SeaweedFS + Redis, tanpa PVC), izin/share, kolaborasi realtime, riwayat/revert, versi, komentar, ekspor, audit, MCP server, dan AI sebagai kolaborator terotorisasi. Deploy GitOps (Jenkins → Harbor → ArgoCD) dan 3 suite E2E Chrome nyata lulus (25 + 44 + 43 cek). Kantor RHEL/RKE AMD64: UNVERIFIED (langkah: `lab/docs/office-sync/DRAWIO-PHASE2-8-OFFICE-STEPS.md`).**

| Dokumen | Isi |
|---|---|
| [docs/MASTER-PLAN.md](docs/MASTER-PLAN.md) | rencana induk, roadmap per fase, status matrix, keputusan review |
| [docs/adr/](docs/adr/README.md) | ADR-0001 … ADR-0012 |
| [docs/spikes/SPIKE-000-drawio-image-embed.md](docs/spikes/SPIKE-000-drawio-image-embed.md) | image & dasar embed |
| [docs/spikes/SPIKE-001-drawio-change-model.md](docs/spikes/SPIKE-001-drawio-change-model.md) | change model, konflik, benchmark (dengan koreksi) |
| [docs/spikes/SPIKE-002-cross-platform.md](docs/spikes/SPIKE-002-cross-platform.md) | ARM64 ↔ AMD64, re-verifikasi, fuzz, checksum, PostgreSQL, gap RHEL/RKE |
| [spikes/](spikes/README.md) | prototipe buang-pakai (**BUKAN KODE PRODUKSI**) |

## Kode produksi

| Path | Isi |
|---|---|
| `cmd/gateway`, `internal/edge`, `internal/identity`, `internal/session`, `internal/config` | gateway: OIDC (code + PKCE, verifikasi JWKS ID token & access token), sesi server-side di Redis, CSRF/origin, rate limit, proxy `/editor/*` ke draw.io resmi, host page + adapter stub, token internal EdDSA ≤ 5 menit |
| `cmd/platform`, `internal/platform`, `internal/model` | platform: dokumen, PDP, op log berurutan (`FOR UPDATE`), WebSocket kolaborasi (`/ws/v1/documents/{id}`), REST `/api/v1`, versi, komentar, audit, presence; model dokumen + kontrak operasi semantik (ADR-0010) |
| `cmd/mcp-server`, `internal/mcpsrv` | MCP (Streamable HTTP, OAuth bearer Keycloak, 15 tool + resources) → REST platform sebagai aktor AI terdelegasi |
| `internal/edge/web/{model,bridge,adapter,host}.js` | browser: model klien (cermin Go), diff/patch ke draw.io resmi, mesin sinkron, UI (daftar, share, riwayat, versi, komentar, ekspor, presence) |
| `webtest/` | konformansi JS↔Go (vektor dari Go, 1799 kasus diff) |
| `Dockerfile` | satu Dockerfile multi-target (`SERVICE_NAME`), `CGO_ENABLED=0`, scratch, uid 65532 |
| `Jenkinsfile` | lint, test, govulncheck, audit CGO → Kaniko → Harbor `drawio-custom-service` → bump `image.tag` **dan** `image.digest` di helm-values-lab |
| `e2e/` | E2E Chrome nyata terhadap lab: `phase2_gateway.mjs` (25), `phase5_collab.mjs` (44: izin, kolaborasi 3 pengguna, revert/restore, restart pod, revoke), `phase6_mcp.mjs` (43: MCP + AI), `phase7_presence.mjs` (51: presence), `phase8_ui.mjs` (9: dialog, share, riwayat live), `phase9_local.mjs` (login page, akun lokal, user management, shell, copy PNG, leave guard). Login Keycloak memakai flow `drawio-browser-role-otp` (role `drawio-access` + OTP): suite mendaftarkan TOTP user uji otomatis (seed di `e2e/.otp-seeds.json`, git-ignored). Password uji dibaca dari `lab/SECRETS.md`. `LOCAL_WEB=1` melayani aset host dari working tree (loop cepat tanpa build) |
| `scripts/dev-image.sh` | build multi-arch lokal + push ke Harbor lab lewat bastion (hanya sampai Jenkins membangun repo ini) |
| `deploy/nginx/drawio.lab.local.conf` | vhost nginx lab (TLS edge) satu-satunya jalan masuk |

Gerbang lokal (sama dengan Jenkins): `golangci-lint run ./cmd/... ./internal/...`, `go vet ./...`, `go test -race ./...`, `govulncheck ./...`, audit CGO.
Tidak diwarisi dari n8n-custom-service: `InsecureSkipVerify`, penerimaan ID token tanpa verifikasi tanda tangan.

## Menghubungkan agen AI (MCP)
Endpoint `https://drawio.lab.local/mcp` (Streamable HTTP, JSON-RPC). Metadata OAuth: `/.well-known/oauth-protected-resource` → Keycloak realm `lab`. Satu client Keycloak per aplikasi agen (`scripts/setup_keycloak_mcp.py`: `drawio-mcp-claude` → aktor `ai-claude`); agen masuk sebagai **pengguna sungguhan** dan bekerja atas namanya. Agar agen melihat sebuah diagram, pemilik: (1) Share → centang "Allow AI collaborators", (2) bagikan ke `ai-claude` sebagai editor. Batas: ops/menit dan penghapusan per request diatur pemilik; semua perubahan AI tercatat (aktor, agen, model) dan bisa di-revert dari History.

## Login, akun lokal, dan tampilan (DrawSpace)
- **Halaman login sendiri** (`/login`):
  - **Logo Vector SVG DrawSpace resmi**: pure vector SVG tanpa ketergantungan font/raster eksternal (`/login-assets/drawspace-logo.svg`, `/login-assets/drawspace-logo-dark.svg`, `/login-assets/drawspace-icon.svg`, `/login-assets/drawspace-wordmark.svg`, `/favicon.svg`) direkonstruksi langsung dari geometri `logo/drawspace.png` (warna `#1463F3`, `#0D5FEF`, `#FF151B`, `#0B1B36`/`#FFFFFF`, `#8294AD`/`#94A3B8`).
  - **Mode Tema Day / Night (Tombol Ikon Kotak seperti Portal Platform)**: tombol kotak icon-only (`36px × 36px`, Lucide `sun` / `moon`, tanpa label teks) di kanan atas halaman login dengan transisi tema halus (`@property` CSS color variables + `340ms ease-in-out`). Saat `/login` dibuka, mode awal dipilih secara acak (`light` atau `dark`, menyesuaikan logo `drawspace-logo.svg` / `drawspace-logo-dark.svg`) dan dapat diubah oleh pengguna (tidak disimpan untuk halaman login).
  - **Dismissible Error Card (Auto-Close 3 Detik)**: kartu merah pesan gagal login (`Invalid username or password.`) dapat ditutup dengan menekan tombol `✕`, mengklik di luar kartu, atau tertutup otomatis setelah 3 detik.
  - **Footer Copyright BTN**: menampilkan `© 2026 PT Bank Tabungan Negara (Persero) Tbk — Tim Platform Engineering`.
  - **Sign in with AD BTN (Utama)**: form username dan password terhubung langsung ke Keycloak via Direct Access Grant (`POST /login/ad`). Menggunakan alur autentikasi khusus `drawio-direct-grant` (diikat pada `authenticationFlowBindingOverrides["direct_grant"]` client `drawio-lab`) agar pengguna yang memiliki OTP terdaftar di Keycloak (seperti `lab-admin`) tetap dapat login langsung dengan username & password tanpa gagal `invalid_grant`.
  - **Sign in with local account (Sekunder)**: tombol sekunder untuk login akun lokal (break-glass / administrator) melalui `POST /login/local`.
  - Akun lokal berperan `user` atau `admin`, dikelola admin di menu pengguna → **Manage users** (`/admin/users`), bisa berbagi diagram dengan pengguna SSO dan sebaliknya. Admin pertama dibuat dari Secret `LOCAL_ADMIN_PASSWORD` (`admin`). ADR-0014.
- **Satu header & menu bar**:
  - **Logo putih di header biru & penghilangan ikon oranye draw.io**: ikon vector DrawSpace pada header biru (`#1868db`) menggunakan warna putih (`#FFFFFF`) dengan aksen kotak merah (`#FF151B`), dan ikon kotak oranye bawaan draw.io (`.geSmallAppIcon`) di kiri menu `File` dihapus via DOM tanpa menyentuh image `jgraph/drawio`.
  - **Input judul diagram terlihat jelas**: kotak nama diagram (`#title`) secara default memiliki latar biru gelap semi-transparan, outline putih halus, dan ikon pensil di sisi kanan agar pengguna langsung tahu judul dapat diklik untuk diedit.
  - **Menu `History` dan `AI` di kanan `Help` & Panel Samping Tidak Menutup Menu Bar**: menu bar draw.io menampilkan `File  Edit  View  Arrange  Extras  Help  History  AI` sepanjang 100% lebar layar; panel samping kanan (`History` / `Versions`) diposisikan di bawah baris menu biru (`top: 40px`) sehingga tidak pernah memotong baris menu bar.
  - **Pengaturan Tema Tersimpan di Menu Akun (Transisi Halus)**: di dalam dropdown akun pengguna (tempat tombol Sign out berada) terdapat pemilih **Theme (`Day` / `Night`)** yang tersimpan di `localStorage` (`drawspace_theme`), memiliki animasi transisi warna halus (`340ms ease-in-out` via `@property`), dan otomatis disinkronkan ke kanvas draw.io. Dropdown akun tertutup otomatis saat klik di luar area (termasuk klik di dalam kanvas/iframe draw.io) tanpa tombol `✕`.
  - **Dialog Share & AI Collaborators**: semua pop-up dialog dilengkapi tombol `✕` (`#dlg-close`) di kanan atas dan juga tertutup saat klik di area luar/backdrop. Dialog **AI Collaborators** menggunakan ikon **Lucide `info`** (`<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>`) untuk menampilkan tooltip penjelasan saat hover.
  - **History & Versions dengan Preview Visual Akurat & Revert Cerdas (Gaya n8n)**: daftar History dan Versions ditampilkan dalam kartu terstruktur (aktor, nomor revisi `#seq`, badge operasi berwarna `+ addCell`, `↔ setPos`, `✎ setProp`, `↗ connect`, `− deleteCell`, `↺ restoreCells`). Klik item atau tombol **Preview** membuka modal pratinjau lebar dengan tab **Diff (Before → After)**, **After (`#toSeq`)**, dan **Before (`#fromSeq-1`)**, me-render bentuk nyata (`ellipse`, `rhombus`, `umlActor`, `swimlane`/container beserta koordinat relatif anak, `text`, `rect`, konektor `src`/`tgt` & floating `gex`, serta bayangan hantu + panah gerak ungu untuk `setPos`) dan rincian perubahan sebelum menekan **Revert this change** atau **Restore to `#toSeq`**.

- **Salin sebagai gambar**: pilih bentuk → klik kanan atau menu Edit → *Copy as PNG image* / *Copy as SVG* (clipboard browser hanya menerima PNG dan SVG) dan *Save selection as JPG…* (unduh).
- **Tanpa pop-up bawaan browser saat keluar** bila semua perubahan sudah tersimpan; dialog sendiri hanya muncul bila masih ada perubahan yang belum sampai ke server.

## Riwayat perubahan penting
- **2026-10-27 (pagi 2)**: Sinkronisasi Restore Versi & Transisi Tema Day/Night Iframe (`adapter.js`, `host.js`, `host.css`, `host.html`):
  1. **Restore Versi Sinkron & Seketika**: mengganti `postMessage` asinkron pada `loadFull(xml)` dengan pemanggilan `ui.setFileData(xml)` langsung pada instance UI draw.io editor, dan memicu `adapter.resync('restore'/'revert')` via WebSocket (reconnect 50ms) saat menekan tombol Restore/Revert pada Version maupun History sehingga kanvas langsung kembali ke state revisi yang dipilih tanpa perlu reload browser.
  2. **Transisi Tema Day/Night Serempak & Halus**: menerapkan pola transisi Portal Platform (`html.theme-transition`) dengan element transitions 320ms pada host shell dan menyuntikkan style transisi ke dokumen iframe draw.io (`.geEditor, .geMenubarContainer, .geSidebarContainer, .geDiagramContainer, .geBackgroundPage`) sehingga pergantian mode tema berlangsung serempak dan halus di seluruh layar.
  3. **Rendering SVG Bentuk Silinder (`cylinder`)**: melengkapi diagram preview SVG modal snapshot untuk mengenali dan merender bentuk tabung/database (`cylinder`/`cylinder3`) dan meluruskan posisi teks label.
- **2026-10-27 (pagi)**: Perbaikan Akurasi Visual Preview History/Versions, Tombol Revert/Restore, Ikon Lucide, & Transisi Tema Halus:
  1. **Pratinjau Visual SVG Akurat (`host.js`)**: membaca properti `StateCell` yang sebenarnya (`gx`, `gy`, `gw`, `gh`, resolusi koordinat absolut hierarki container/parent `c.p`, `sn`/`st:shape`, `v`/`va:label`, `src`/`tgt`/`gex`) dan field `model.Op` (`t`, `cell`, `key`, `val`, `term`, `r`), sehingga bentuk `ellipse`, `rhombus`, `umlActor` (stick-figure), `swimlane`/list, `text`, `rect`, serta garis konektor dirender sesuai bentuk dan posisi aslinya di kanvas. Menambahkan tab **Diff (Before → After)** (dengan ghost shape + panah pergerakan untuk `setPos`), **After (`#toSeq`)**, dan **Before (`#fromSeq-1`)**.
  2. **Perbaikan Revert & Restore (`internal/model/revert.go`, `internal/platform/api.go`)**: menangani `OpRestore` (`restoreCells`) dan batch `restore`, menghidupkan kembali sel yang sudah terhapus saat me-revert `addCell`/`setPos`/`setProp`, mendukung toggle dua arah (`deleteCell`/`setPos`/`setProp`), fallback otomatis ke state revisi target bila operasi tidak lagi berlaku pada head, serta tombol **Restore to `#toSeq`** langsung dari modal Preview.
  3. **Tombol Tema Login Icon-Only & Transisi Halus (`login.html`, `login.css`, `login.js`, `host.css`, `host.js`)**: mengubah tombol tema di `/login` menjadi tombol kotak icon-only (`36px × 36px`, Lucide `sun` / `moon`, tanpa teks `Day mode` / `Night mode`) dengan animasi `.pop` dan transisi warna `@property` (`340ms ease-in-out`) seperti Portal Platform.
  4. **Ikon Lucide `info` pada AI Collaborators (`host.js`, `host.css`)**: menggunakan ikon resmi Lucide `info` (`circle` + `path`) pada setiap pengaturan di pop-up AI Collaborators.
- **2026-10-27 (dini hari)**: Peningkatan Tema Day/Night, Preview History/Versions Gaya n8n, & Penyempurnaan UX:
  1. **Footer Login & Toggle Day/Night Mode**: penggantian teks footer login menjadi `© 2026 PT Bank Tabungan Negara (Persero) Tbk / Tim Platform Engineering`, penambahan tombol Day/Night mode di kanan atas `/login` (mode awal random saat buka `/login`, tidak disimpan), serta penambahan pengaturan Theme (`Day` / `Night`) di dropdown menu akun setelah login yang tersimpan permanen di `localStorage` (`drawspace_theme`).
  2. **Kartu Error Login & Penutupan Pop-up/Dropdown**: kartu error login (`Invalid username or password.`) kini memiliki tombol `✕`, dapat ditutup dengan klik di mana saja, dan otomatis tertutup setelah 3 detik. Pop-up dialog (`Share`, `AI`, `Preview`, dll.) dilengkapi tombol `✕` (`#dlg-close`) dan klik luar (backdrop); dropdown akun tertutup otomatis saat klik di luar termasuk di area kanvas `<iframe id="editor">`.
  3. **Panel History/Versions & Penghilangan Ikon Oranye draw.io**: panel samping kanan (`#panel`) diposisikan mulai `top: 40px` agar tidak menutup baris menu bar biru draw.io, dan ikon kotak oranye draw.io (`.geSmallAppIcon`) di kiri menu `File` disembunyikan via DOM tanpa mengubah image asli.
  4. **History & Versions Rapi + Preview Sebelum Apply (Selevel n8n)**: kartu riwayat menampilkan aktor, revisi `#seq`, dan badge operasi berwarna; penambahan endpoint `GET /api/v1/documents/{id}/history/{batch}` dan `GET /api/v1/documents/{id}/versions/{vid}` serta modal **Preview** visual SVG + daftar diff atribut sebelum → sesudah sebelum melakukan Revert/Restore.
  5. **Ikon Info `i` pada AI Collaborators**: penggantian ikon `?` bulat dengan badge ikon **`i`** SVG yang lebih ramping dan menyatu dengan tema.
- **2026-10-26 (malam)**: Perbaikan UI Editor, Header, Menu Bar (`History` & `AI`), dan Dialog Share:
  1. Perbaikan posisi menu **History** (`className = 'geItem'`, mengatasi bug draw.io `ui=atlas` yang memberi `class="geMenubar"` absolut sehingga menumpuk tombol `File`) dan penempatan di sebelah kanan **Help**.
  2. Penambahan menu **AI** di sebelah kanan **History** (`File Edit View Arrange Extras Help History AI`) yang membuka pop-up pengaturan **AI Collaborators** tersendiri beserta ikon **`?`** (tooltip penjelasan saat hover) di setiap pengaturan.
  3. Perubahan warna ikon logo DrawSpace di header biru menjadi putih (`#FFFFFF`) + aksen merah (`#FF151B`), serta penambahan outline, background kontras samar, dan ikon pensil pada input judul diagram (`#title`).
  4. Penghapusan opsi `commenter` pada dialog **Share**, pembuatan custom themed dropdown (`viewer` / `editor`), dan penambahan tombol **`+ Add user`** di bawah daftar user yang memiliki akses.
- **2026-10-26**: Pembaruan halaman login DrawSpace & Rekonstruksi Vector SVG:
  1. Rekonstruksi logo DrawSpace menjadi pure vector SVG (`drawspace-logo.svg`, `drawspace-logo-dark.svg`, `drawspace-icon.svg`, `drawspace-wordmark.svg`, serta ekspor PNG ukuran 1024/512/256/64/32 di `logo/`) sesuai spesifikasi geometri dan warna `logo/drawspace.png`, diintegrasikan ke `/login`, `/logged-out`, `/favicon.svg`, dan header editor `host.html`.
  2. Integrasi direct SSO Keycloak: tombol utama biru diubah menjadi **"Sign in with AD BTN"** (`POST /login/ad`) yang mengeksekusi direct access grant ke Keycloak sehingga pengguna login langsung tanpa dialihkan ke halaman web Keycloak.
  3. Tombol bawah disesuaikan menjadi **"Sign in with local account"** (`POST /login/local`) untuk akun lokal / break-glass.
  4. Pengaktifan `directAccessGrantsEnabled: true` dan pengikatan flow `drawio-direct-grant` pada `authenticationFlowBindingOverrides["direct_grant"]` client Keycloak `drawio-lab` (`scripts/setup_keycloak_flow.py`).
- **2026-10-27**: Penyempurnaan Direct Grant & Conditional Admin OTP:
  1. Penghapusan override Browser Flow pada client Keycloak `drawio-lab` karena DrawSpace murni menggunakan Direct Grant tanpa browser redirect.
  2. Implementasi subflow conditional OTP pada `drawio-direct-grant`: pengguna biasa login hanya dengan username & password, sedangkan pengguna dengan client role `admin` wajib memasukkan kode TOTP 6-digit.
  3. Penyesuaian `internal/edge/oidc.go` untuk menangani deskripsi error Keycloak `Invalid authenticator code` menjadi tantangan `totp_required` bagi form web UI.

## Presence langsung (siapa sedang apa)
Saat beberapa orang membuka diagram yang sama, setiap orang melihat **siapa memilih / mengedit / menggeser / mengubah ukuran apa**: kotak berwarna per pengguna + label "Nama · editing", hantu putus-putus saat drag/resize, dan teks yang sedang diketik (draft) di dekat sel. AI yang sedang menerapkan perubahan tampil sebagai "AI … applying changes". Ini **ephemeral** (WebSocket + Redis, TTL 8 dtk, tanpa DB/riwayat/lock): presence memberi tahu, lapisan operasi semantik yang menyelesaikan konflik. Desain/bukti: `docs/spikes/SPIKE-004-live-presence.md`, `docs/adr/0013-live-presence-ephemeral.md`, `e2e/phase7_presence.mjs`. Setelah rilis baru, muat ulang tab yang sudah terbuka sekali.

## Performa
`node e2e/perf_collab.mjs` (lab, 2026-10-25, diagram 500 sel, dua Chrome): server menerima batch 500 `addCell` dalam 87 ms; buka+render 0,7 dtk; latensi perubahan satu sel dari browser A ke B p50 36 ms / p95 42 ms / maks 60 ms (25 sampel; kriteria ADR-0006 #5 p95 < 250 ms **terpenuhi**); ubah gaya 100 sel sekaligus tampil di B dalam 94 ms; hash editor == server setelah semburan. Keterbatasan: satu pasang klien di satu mesin, jaringan lab, belum uji >2 klien aktif menulis bersamaan atau diagram >500 sel.

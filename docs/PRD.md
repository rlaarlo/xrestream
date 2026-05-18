# PRD: HLS Proxy Restream Web App
**Versi:** 3.0  
**Tanggal Revisi:** Mei 2026  
**Perubahan Utama:** Sinkronisasi dengan implementasi aktual — penambahan Transmux Mode (default), arsitektur multi-node terdistribusi, multi-user dengan RBAC, integrasi Cloudflare R2, share/embed link, dan manajemen per-user origin + R2.

---

## 1. Ringkasan Produk

Produk ini adalah web app untuk membuat link HLS proxy/relay dari sumber HLS asli. User memasukkan URL sumber `.m3u8`, lalu sistem menghasilkan link baru dari domain sendiri yang dapat diputar di player HLS umum.

Target akhir:

```
https://stream.domain.com/proxy/{channelSlug}/index.m3u8
```

Link tersebut harus dapat diputar di player yang mendukung HLS, seperti VLC, hls.js, video.js, Safari, IPTV player, atau player lain yang menerima URL `.m3u8`.

---

## 2. Tujuan

1. Menghasilkan link `.m3u8` relay/proxy dari input HLS source atau stream langsung (RTMP/TS).
2. Menyembunyikan URL source asli dari client/player.
3. **Membatasi koneksi ke source hanya 1 koneksi per channel** via Ingest Mode.
4. Mendukung konversi stream non-HLS (TS, RTMP) ke HLS via FFmpeg (Transmux Mode).
5. Menyediakan dashboard multi-user untuk membuat, mengelola, memantau, dan menonaktifkan channel.
6. Menyediakan API backend yang dapat dipakai frontend self-hosted maupun Cloudflare Pages.
7. Menyimpan konfigurasi channel dan status di Neon Postgres.
8. Mendukung arsitektur terdistribusi: satu control plane + banyak node agent di berbagai VPS.
9. Mendukung sinkronisasi segment ke Cloudflare R2 sebagai CDN opsional.

---

## 3. Mode Operasi Channel

Sistem mendukung **tiga mode** operasi per channel. **Mode default adalah Transmux Mode.**

### 3.1 Transmux Mode (Default)

Backend menerima stream langsung (TS, RTMP, atau sumber non-HLS lain), lalu menggunakan FFmpeg `-c copy` (tanpa re-encode) untuk mengkonversi ke HLS. 1 koneksi ke source.

```
Source Stream (TS / RTMP / non-HLS)
    ↑
    | ← 1 koneksi (FFmpeg process)
    |
Go Backend [Transmux Worker]
    |
    ├── FFmpeg -c copy → segment .ts + playlist .m3u8
    ├── Simpan ke disk cache
    └── Serve player dari cache lokal

Player 1 ─┐
Player 2 ─┤──> Go Backend (baca dari disk cache)
Player 3 ─┘
```

**Karakteristik:**

| Aspek | Nilai |
|---|---|
| Input | TS, RTMP, atau URL stream non-HLS |
| Proses | FFmpeg `-c copy` (tidak ada re-encode) |
| Koneksi ke source | **1 per channel** |
| Output | HLS `.m3u8` + segment `.ts` |
| Cocok untuk | Stream langsung non-HLS, IPTV TS |

### 3.2 Ingest Mode (Recommended untuk HLS Source)

Backend secara aktif melakukan fetch ke source HLS tanpa menunggu request dari player.

```
Source HLS
    ↑
    | ← 1 koneksi saja (loop internal backend)
    |
Go Backend [Ingest Worker]
    |
    ├── Poll playlist tiap N detik
    ├── Detect segment baru
    ├── Fetch & simpan segment ke disk cache
    └── Serve semua player dari cache lokal

Player 1 ─┐
Player 2 ─┤──> Go Backend (hanya baca dari cache, tidak trigger upstream)
Player 3 ─┘
```

**Karakteristik:**

| Aspek | Nilai |
|---|---|
| Koneksi ke source | **1 koneksi permanen per channel** |
| Player memicu upstream | **Tidak pernah** |
| Latency tambahan | +1 playlist cycle (~2–4 detik dari live edge) |
| Cocok untuk | Live IPTV HLS, stream publik banyak viewer |
| Beban source | Sangat minimal |

### 3.3 Proxy Mode (On-Demand)

Backend hanya fetch ke source saat ada request dari player. Dilengkapi request coalescing untuk mencegah duplicate fetch.

```
Player request segment
    ↓
Go Backend
    ├── Cache hit? → kirim dari cache
    └── Cache miss? → fetch 1x ke source (coalescing aktif)
```

**Karakteristik:**

| Aspek | Nilai |
|---|---|
| Koneksi ke source | On-demand, 1 per segment unik (via coalescing) |
| Player memicu upstream | Ya, jika cache miss |
| Latency | Mengikuti source (lebih rendah dari ingest) |
| Cocok untuk | Channel idle rendah viewer, debugging |
| Beban source | Lebih tinggi dari ingest mode |

---

## 4. Stack Teknis

**Frontend:**
```
SvelteKit + video.js + @iconify/svelte
Self-hosted via Docker (Nginx) atau Cloudflare Pages
```

**Backend:**
```
Go API di VPS atau Docker Compose
Mode: control (default) atau node (agent)
```

**Database:**
```
Neon Postgres (atau Postgres lainnya)
```

**Proxy/Serve:**
```
Go HLS relay handler + ingest worker
Transmux: FFmpeg -c copy (tanpa re-encode)
Nginx sebagai reverse proxy HTTPS
```

**Cache:**
```
In-memory cache untuk playlist
Disk cache untuk segment (lokal VPS)
Request coalescing via singleflight (proxy mode)
Cloudflare R2 optional (segment CDN)
```

**Deployment:**
```
Docker Compose (self-hosted, satu node)
Atau: Control Plane VPS + banyak Node Agent VPS
```

---

## 5. Arsitektur Sistem

### 5.1 Deployment Mode A: Single Node (Docker Compose)

```
[Source HLS / TS / RTMP]
       ↑
       |
[Docker: Go Backend]
   ├── Relay/Ingest/Transmux Worker
   ├── Neon Postgres
   └── Docker Volume: /data/cache

[Docker: Frontend (Nginx)]
   └── SvelteKit build

[Nginx Reverse Proxy — HTTPS]
   ├── api.domain.com  → Backend :2087
   └── app.domain.com  → Frontend :80
```

### 5.2 Deployment Mode B: Control Plane + Node Agents

```
[Control Plane VPS]
   ├── Go Backend (mode=control)
   ├── Neon Postgres
   └── Serve binary agent via /agent/<name>

[Node Agent VPS-1]           [Node Agent VPS-2]
   ├── Go Backend (mode=node)   ├── Go Backend (mode=node)
   ├── Pull config dari CP      ├── Pull config dari CP
   ├── Kirim heartbeat          ├── Kirim heartbeat
   └── Relay/Transmux lokal     └── Relay/Transmux lokal

[Admin Browser]
   └── Frontend → Control Plane API
         ├── Assign channel ke node tertentu
         └── Monitor status semua node
```

**Komunikasi node:**
```
POST /node/heartbeat  → node kirim heartbeat ke CP
GET  /node/config     → node ambil konfigurasi channel dari CP
POST /node/report     → node kirim metrik ke CP
GET  /agent/<binary>  → node download binary terbaru dari CP
```

### 5.3 Ingest Mode Worker

```
[Source HLS Server]
       ↑
       | ← 1 koneksi per channel
       |
[Go Backend]
   ├── Ingest Worker (goroutine per channel aktif)
   │     ├── Poll playlist tiap N detik
   │     ├── Fetch segment baru → disk cache
   │     ├── Optional sync ke R2 (jika sync_enabled)
   │     └── Update in-memory playlist (rewritten)
   │
   └── HTTP Server
         ├── /proxy/{slug}/index.m3u8 → serve dari memory
         └── /proxy/{slug}/asset?ref=...&sig=... → serve dari disk/R2
```

### 5.4 Transmux Mode Worker

```
[Source TS / RTMP / non-HLS]
       ↑
       | ← 1 koneksi (FFmpeg)
       |
[Go Backend]
   ├── Transmux Worker (goroutine per channel aktif)
   │     ├── Spawn FFmpeg -c copy
   │     ├── Output: segment .ts + index.m3u8 di disk
   │     └── Cleanup segment lama
   │
   └── HTTP Server
         ├── /proxy/{slug}/index.m3u8 → serve dari disk
         └── /proxy/{slug}/asset?ref=...&sig=... → serve segment .ts
```

### 5.5 Proxy Mode

```
[Player / IPTV]
       |
       v
[Go Backend]
   ├── Cache hit → serve langsung dari disk
   └── Cache miss → singleflight fetch source → cache → serve

[Source HLS Server] ← hanya saat cache miss
```

### 5.6 Admin Frontend

```
Admin Browser
   ↓
SvelteKit Frontend (app.domain.com)
   ↓
Go API (api.domain.com)
   ↓
Neon Postgres
```

**Domain yang disarankan:**

```
app.domain.com    → SvelteKit Frontend
api.domain.com    → Go API Backend
stream.domain.com → HLS relay output (sama dengan api atau node terpisah)
```

---

## 6. Cara Kerja Ingest Worker

### 6.1 Lifecycle Worker

```
Channel status = active, mode = ingest atau transmux
    ↓
Worker start (goroutine)
    ↓
Loop:
  1. Fetch playlist source (ingest) atau spawn FFmpeg (transmux)
  2. Parse segment list
  3. Diff dengan seen segments
  4. Fetch/generate segment baru → disk cache
  5. Optional: sync ke R2 jika sync_enabled
  6. Rewrite playlist → simpan ke memory
  7. Sleep(pollInterval)
    ↓
Channel status = disabled / error
    ↓
Worker stop (context cancel)
```

### 6.2 Segment Fetch di Ingest Mode

Worker mem-fetch segment baru ke disk cache. Setelah segment tersedia di cache, jika `sync_enabled=true` dan `sync_delay_seconds` terpenuhi, segment diunggah ke Cloudflare R2 dan URL publik R2 dipakai sebagai signed URL.

### 6.3 Playlist Serve di Ingest Mode

```
GET /proxy/channel-1/index.m3u8
    ↓
Baca playlist rewritten dari memory cache
    ↓
Return ke player (tidak ada fetch ke source sama sekali)
```

### 6.4 Segment Serve di Ingest Mode

```
GET /proxy/channel-1/asset?ref={ref}&sig={signature}
    ↓
Validasi signature HMAC
    ↓
Jika R2 ready (sync_enabled) → redirect / serve dari R2 public URL
Jika tidak → lookup file di disk cache
    ↓
Jika ada → stream ke player
Jika tidak ada (belum di-fetch worker) → poll 250ms hingga 5 detik, lalu 404
```
---

## 7. Cara Kerja HLS Proxy Mode

### 7.1 Playlist Request

```
GET /proxy/channel-1/index.m3u8
    ↓
Ambil channel dari Neon
    ↓
Cek playlist memory cache (TTL 2 detik)
    ├── Hit → return playlist rewritten
    └── Miss → fetch dari source → rewrite → cache → return
```

### 7.2 Segment Request

```
GET /proxy/channel-1/asset?ref={ref}&sig={signature}
    ↓
Validasi signature HMAC
    ↓
Cek disk cache
    ├── Hit → stream ke player
    └── Miss → singleflight (golang.org/x/sync/singleflight):
          ├── Hanya 1 fetch aktif per ref
          └── Fetch dari source → cache → stream
```

---

## 8. Request Coalescing (Proxy Mode)

Request coalescing wajib untuk segment di proxy mode.

**Masalah yang diselesaikan:**

```
100 viewer request segment yang sama hampir bersamaan.
Tanpa coalescing → 100 fetch ke source.
Dengan coalescing → 1 fetch, 99 request menunggu hasil.
```

**Perilaku yang diharapkan:**

1. Key coalescing berdasarkan URL source segment final.
2. Hanya satu request upstream aktif untuk key yang sama.
3. Request lain menunggu hasil atau memakai cache setelah fetch selesai.
4. Jika upstream gagal, semua waiting request menerima error yang konsisten.
5. Timeout upstream harus dibatasi.

> **Catatan:** Di Ingest Mode, coalescing tidak diperlukan karena player tidak pernah memicu fetch ke source.

---

## 9. Cache Policy

### 9.1 Playlist Cache

| Aspek | Transmux Mode | Ingest Mode | Proxy Mode |
|---|---|---|---|
| Storage | Disk | Memory | Memory |
| TTL | Berdasarkan segment terbaru | Di-update oleh worker | 1–3 detik |
| Isi | FFmpeg HLS output | Playlist rewritten terbaru | Playlist fetch terakhir |

### 9.2 Segment Cache

| Aspek | Transmux Mode | Ingest Mode | Proxy Mode |
|---|---|---|---|
| Storage | Disk lokal | Disk lokal | Disk lokal |
| TTL | `LOCAL_RETENTION_SECONDS` (default 60s) | `segment_ttl_seconds` (default 120s) | `segment_ttl_seconds` (default 120s) |
| Trigger | FFmpeg output | Ingest Worker | Player request (cache miss) |
| R2 Sync | Opsional | Opsional | Tidak |

### 9.3 Cache Key

```
HMAC-SHA256(signingSecret, sourceSegmentURL) → ref
```

Ref digunakan sebagai nama file di disk cache dan sebagai parameter `?ref=` di signed URL.

### 9.4 Cache Behavior

1. Segment immutable → TTL lebih panjang aman.
2. Cleanup worker berjalan periodik untuk menghapus segment kadaluarsa.
3. Cache bisa dimatikan per channel (`cache_enabled=false`) untuk debugging.
4. R2 sync: jika `sync_enabled=true`, segment diunggah ke R2 setelah `sync_delay_seconds`.
5. Jika R2 ready, signed URL diganti URL publik R2 → mengurangi beban bandwidth VPS.

**Env default:**

```
LOCAL_RETENTION_SECONDS   = 60
R2_RETENTION_SECONDS      = 600
SOURCE_ERROR_AFTER_SECONDS = 300
UPSTREAM_TIMEOUT_MS       = 15000
MAX_WORKERS               = 20
```

**Default per channel:**

```
playlist_ttl_seconds   = 2       (proxy mode)
segment_ttl_seconds    = 120
ingest_poll_seconds    = 2
sync_enabled           = false
sync_delay_seconds     = 30
```

---

## 10. Requirement Fungsional

### 10.1 Channel Management

User (admin/viewer sesuai role) dapat:

1. Membuat channel baru (nama, slug, URL source, mode, node target).
2. Memilih mode: **transmux** (default), **ingest**, atau **proxy**.
3. Mengisi custom HTTP header per channel: `Referer`, `User-Agent`, `Origin` untuk source request.
4. Mengaktifkan atau menonaktifkan channel.
5. Melihat generated proxy URL dan tombol copy.
6. Memilih apakah playback token diperlukan (`playback_token_required`).
7. Mengaktifkan R2 sync per channel (`sync_enabled`, `sync_delay_seconds`).
8. Menghapus channel dan purge cache.
9. Menetapkan channel ke node tertentu (`node_id`).

### 10.2 Worker Management

System harus:

1. Menjalankan worker (transmux/ingest) saat channel mode bukan proxy dan status=active.
2. Menghentikan worker saat channel dinonaktifkan, dihapus, atau mode diganti.
3. Tidak memulai worker duplikat untuk channel yang sama.
4. Menampilkan status worker di dashboard (running/stopped/error).
5. Mencatat error source di database (`last_error`, `last_source_status`).
6. Membatasi jumlah worker aktif (`MAX_WORKERS`).

### 10.3 Proxy Playback

System harus:

1. Menghasilkan URL `.m3u8` dengan HMAC-signed asset URL.
2. Me-rewrite semua segment URL (relatif/absolut) di playlist.
3. Me-rewrite URI attribute di tag `#EXT-X-KEY`, `#EXT-X-MAP`, `#EXT-X-MEDIA`, dll.
4. Me-rewrite sub-playlist URL jika source adalah master playlist.
5. Memberikan header CORS sesuai daftar allowed origins per user.
6. Mengembalikan MIME type yang benar.

**MIME type minimal:**

```
.m3u8 → application/vnd.apple.mpegurl
.ts   → video/mp2t
.m4s  → video/iso.segment
.mp4  → video/mp4
.key  → application/octet-stream
```

### 10.4 Share & Embed

System harus:

1. Menghasilkan share link dengan TTL tertentu (`/share/{slug}?sig=...&exp=...`).
2. Menyediakan embed page (`/embed/{slug}`) dengan video player (video.js) built-in.
3. Share link dapat dibuat dari dashboard dengan durasi yang dapat diatur.

### 10.5 Multi-User & RBAC

System harus:

1. Mendukung registrasi user (`/auth/signup`, dikendalikan `ALLOW_SIGNUP`).
2. Mendukung login dengan username/password, menghasilkan session token.
3. Role user: `admin` (akses penuh) atau `viewer` (akses terbatas).
4. Admin dapat mengelola semua user (buat, ubah role, enable/disable, ganti password).
5. Setiap user memiliki channel, node, origin, dan R2 config milik sendiri.
6. Admin dapat melihat semua channel dan node dari semua user.
7. Session disimpan di database dengan TTL 24 jam.
8. User yang disabled tidak bisa login.

### 10.6 Node Management

System harus:

1. User dapat mendaftarkan node agent VPS baru dengan nama dan host.
2. API key node dihasilkan satu kali saat pembuatan dan tidak disimpan plain-text.
3. Node agent dapat di-deploy ke VPS lain (binary tersedia via `/agent/<name>`).
4. Control plane memantau status node via heartbeat (online/offline/pending).
5. Node yang tidak heartbeat lebih dari 3× interval secara otomatis ditandai offline.
6. Channel dapat di-assign ke node tertentu; node agent hanya menjalankan channel miliknya.

### 10.7 R2 Cloudflare Integration

System harus:

1. User dapat menyimpan konfigurasi R2 per-user (account ID, access key, bucket, public URL).
2. Secret key disimpan terenkripsi, tidak pernah dikembalikan plain-text ke frontend.
3. Channel yang `sync_enabled=true` mengupload segment ke R2 secara async.
4. Setelah segment R2 ready, URL publik R2 dipakai sebagai signed URL → mengurangi load VPS.
5. Purge channel juga menghapus folder di R2.

### 10.8 Allowed Origins

System harus:

1. User dapat mendaftarkan daftar allowed origins untuk CORS (`/me/origins`).
2. Admin dapat mengelola global origins (`/admin/origins`).
3. Backend memeriksa `Origin` header request terhadap daftar yang aktif.
4. Jika origin tidak terdaftar, CORS header tidak dikirim (atau request ditolak sesuai konfigurasi).

### 10.9 Monitoring

Dashboard harus menampilkan per channel:

1. Status: active, disabled, source_error.
2. Mode: transmux / ingest / proxy.
3. Worker status: running, stopped, error.
4. Proxy/relay URL (dengan tombol copy).
5. Live viewer count (distinct IP, window 60 detik).
6. Last request time & last source fetch status.
7. Metrik: playlist requests, segment requests, upstream requests, cache hits/misses, bytes sent.
8. Error terakhir dengan pesan error.

### 10.10 Security

System harus:

1. Memvalidasi `input_url` hanya HTTP/HTTPS.
2. Menolak URL lokal/private network untuk mencegah SSRF.
3. Session-based auth untuk admin API (token disimpan di database).
4. HMAC-signed URL untuk setiap segment/asset.
5. Optional playback token per channel (`playback_token_required`).
6. CORS origin whitelist per user.
7. Membatasi timeout fetch upstream (`UPSTREAM_TIMEOUT_MS`).

**Private IP yang ditolak untuk source URL:**

```
127.0.0.0/8
10.0.0.0/8
172.16.0.0/12
192.168.0.0/16
169.254.0.0/16
::1/128
fc00::/7
fe80::/10
```

---

## 11. Requirement Non-Fungsional

**Performance target:**

```
Playlist response (ingest/transmux mode) p95 < 50 ms  (serve dari memory/disk)
Playlist response (proxy mode)           p95 < 500 ms (fetch source)
Segment cache hit                        p95 < 200 ms
Segment cache miss                       mengikuti latency source (proxy mode only)
Live viewer count                        refresh window 60 detik
```

**Reliability:**

```
Backend dapat berjalan sebagai systemd service atau Docker container
Auto restart via Docker restart policy atau systemd Restart=always
Graceful shutdown (worker stop sebelum exit, flush report node)
Structured JSON logging (slog)
Worker tidak restart otomatis dalam loop — error dicatat, channel perlu re-enable manual atau dikonfigurasi backoff
```

**Scalability:**

```
Default: MAX_WORKERS = 20 channel aktif bersamaan
Node agent: satu binary yang sama dipakai untuk mode control dan node
Setiap user memiliki node, channel, dan R2 config sendiri → tenant isolation
R2 sync mengurangi beban bandwidth outgoing VPS
```

**Catatan bandwidth:**

```
Transmux/Ingest mode:
  Upstream ke source = 1× bitrate per channel
  Downstream ke viewer = N× bitrate (dari VPS, kecuali R2 sync aktif)

Proxy mode:
  Upstream ke source = tergantung cache hit ratio
  Downstream ke viewer = N× bitrate

R2 sync aktif:
  Downstream ke viewer = dari R2 CDN (tidak membebani VPS)
```

---

## 12. Database Schema

```sql
-- Tabel utama channel
create table channels (
  id                      uuid        primary key default gen_random_uuid(),
  name                    text        not null,
  slug                    text        not null unique,
  input_url               text        not null,
  mode                    text        not null default 'transmux',
    -- 'transmux' = FFmpeg -c copy → HLS (default)
    -- 'ingest'   = active relay HLS (1 koneksi ke source)
    -- 'proxy'    = on-demand fetch (singleflight + cache)
  status                  text        not null default 'active',
    -- 'active', 'disabled', 'source_error'
  worker_status           text        not null default 'stopped',
    -- 'running', 'stopped', 'error'
  playback_token          text,
  playlist_url            text,
  playlist_ttl_seconds    integer     not null default 2,
  segment_ttl_seconds     integer     not null default 120,
  ingest_poll_seconds     integer     not null default 2,
  cache_enabled           boolean     not null default true,
  sync_enabled            boolean     not null default false,
  sync_delay_seconds      integer     not null default 30,
  playback_token_required boolean     not null default true,
  http_referer            text        not null default '',
  http_user_agent         text        not null default '',
  http_origin             text        not null default '',
  owner_id                uuid        references users(id) on delete set null,
  node_id                 uuid        references nodes(id) on delete set null,
  last_request_at         timestamptz,
  last_source_fetch_at    timestamptz,
  last_source_status      integer,
  last_error              text,
  worker_started_at       timestamptz,
  created_at              timestamptz not null default now(),
  updated_at              timestamptz not null default now()
);

-- Metrik per channel per window 1 menit
create table channel_metrics (
  id                  bigserial   primary key,
  channel_id          uuid        not null references channels(id) on delete cascade,
  window_start        timestamptz not null,
  playlist_requests   integer     not null default 0,
  segment_requests    integer     not null default 0,
  upstream_requests   integer     not null default 0,
  cache_hits          integer     not null default 0,
  cache_misses        integer     not null default 0,
  bytes_sent          bigint      not null default 0,
  bytes_upstream      bigint      not null default 0,
  worker_errors       integer     not null default 0,
  created_at          timestamptz not null default now(),
  unique (channel_id, window_start)
);

-- Allowed CORS origins per user
create table allowed_origins (
  id          uuid        primary key default gen_random_uuid(),
  owner_id    uuid        references users(id) on delete cascade,
  origin      text        not null,
  label       text        not null default '',
  enabled     boolean     not null default true,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  unique (coalesce(owner_id, '00000000-0000-0000-0000-000000000000'::uuid), origin)
);

-- User accounts
create table users (
  id            uuid        primary key default gen_random_uuid(),
  username      text        not null unique,
  password_hash text        not null,
  role          text        not null default 'admin',
    -- 'admin', 'viewer'
  enabled       boolean     not null default true,
  last_login_at timestamptz,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now()
);

-- Session tokens
create table sessions (
  token        text        primary key,
  user_id      uuid        not null references users(id) on delete cascade,
  expires_at   timestamptz not null,
  created_at   timestamptz not null default now(),
  last_used_at timestamptz not null default now()
);

-- Node agent registrations
create table nodes (
  id            uuid        primary key default gen_random_uuid(),
  owner_id      uuid        not null references users(id) on delete cascade,
  name          text        not null,
  host          text        not null default '',
  api_key_hash  text        not null,   -- bcrypt hash, plain key shown sekali saat create
  status        text        not null default 'pending',
    -- 'pending', 'online', 'offline'
  last_seen_at  timestamptz,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now()
);

-- Cloudflare R2 config per user
create table r2_configs (
  id                uuid        primary key default gen_random_uuid(),
  owner_id          uuid        not null unique references users(id) on delete cascade,
  account_id        text        not null,
  access_key_id     text        not null,
  secret_access_key text        not null,
  bucket            text        not null,
  public_url        text        not null default '',
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now()
);
```

---

## 13. API Backend

### Auth API

```
POST   /auth/login           ← username+password atau service token
POST   /auth/signup          ← registrasi user baru (jika ALLOW_SIGNUP=true)
GET    /auth/config          ← cek apakah signup diizinkan
POST   /auth/logout          ← invalidasi session
GET    /auth/me              ← info user yang sedang login
```

### Channel API (memerlukan auth)

```
GET    /channels             ← list channel milik user (admin: semua)
POST   /channels             ← buat channel baru
GET    /channels/{id}        ← detail channel
PATCH  /channels/{id}        ← update channel (termasuk start/stop worker via status)
DELETE /channels/{id}        ← hapus channel + purge cache
GET    /channels/{id}/metrics
POST   /channels/{id}/purge-cache
```

### Playback API (public, validasi signature)

```
GET /proxy/{slug}/index.m3u8
GET /proxy/{slug}/asset?ref={ref}&sig={sig}
GET /share/{slug}?sig={sig}&exp={unix}     ← share link dengan TTL
GET /embed/{slug}                          ← embed page dengan video.js player
```

### User Settings API

```
GET    /me/nodes             ← list node agent milik user
POST   /me/nodes             ← daftarkan node baru (api key dikembalikan sekali)
GET    /me/nodes/{id}
PATCH  /me/nodes/{id}
DELETE /me/nodes/{id}

GET    /me/r2                ← konfigurasi R2 milik user
PUT    /me/r2                ← simpan/update R2 config
DELETE /me/r2

GET    /me/origins           ← allowed origins milik user
POST   /me/origins
PATCH  /me/origins/{id}
DELETE /me/origins/{id}
```

### Admin API (memerlukan role admin)

```
GET    /admin/users
POST   /admin/users
GET    /admin/users/{id}
PATCH  /admin/users/{id}
DELETE /admin/users/{id}
PATCH  /admin/users/{id}/password

GET    /admin/origins        ← global allowed origins
POST   /admin/origins
PATCH  /admin/origins/{id}
DELETE /admin/origins/{id}

GET    /admin/nodes          ← semua node dari semua user
```

### Node Agent API (memerlukan X-Node-Key header)

```
POST   /node/heartbeat       ← node kirim heartbeat ke control plane
GET    /node/config          ← node ambil konfigurasi channel + R2 + signing secret
POST   /node/report          ← node kirim metrik ke control plane
```

### Utility

```
GET    /health               ← health check
GET    /agent/{binary-name}  ← download binary node agent (restream-api-linux-amd64/arm64)
```

**Kenapa segment memakai signed URL:**

1. URL source asli tidak bocor ke player.
2. Client tidak bisa membuat proxy request bebas ke domain lain.
3. Backend memastikan asset memang berasal dari playlist channel tersebut.
4. Signature = HMAC-SHA256(signingSecret, ref) — tidak bisa dipalsukan tanpa secret.

---

## 14. URL Output

```
https://stream.domain.com/proxy/{channelSlug}/index.m3u8
```

**Contoh:**

```
https://stream.domain.com/proxy/channel-1/index.m3u8
```

**Share link (dengan TTL):**

```
https://stream.domain.com/share/channel-1?sig=...&exp=1748000000
```

**Embed page:**

```
https://stream.domain.com/embed/channel-1
```

---

## 15. CORS dan Header Playback

```
Access-Control-Allow-Origin:   (dari allowed_origins yang aktif untuk user pemilik channel)
Access-Control-Allow-Methods:  GET, OPTIONS
Access-Control-Allow-Headers:  Range, Origin, Accept, Content-Type
Cache-Control:                 no-cache  (untuk playlist)
Cache-Control:                 public, max-age=120  (untuk segment)
```

CORS origin diperiksa terhadap tabel `allowed_origins`. Jika request tidak membawa `Origin` header atau origin tidak terdaftar dan aktif, CORS header tidak ditambahkan.

---

## 16. Perbandingan Mode

| Aspek | Transmux Mode | Ingest Mode | Proxy Mode |
|---|---|---|---|
| Input | TS / RTMP / non-HLS | HLS `.m3u8` | HLS `.m3u8` |
| Proses | FFmpeg `-c copy` | Poll + fetch segment | On-demand fetch |
| Koneksi ke source | **1 per channel** | **1 per channel** | On-demand (cache miss) |
| Player trigger upstream | **Tidak pernah** | **Tidak pernah** | Ya (jika cache miss) |
| Latency ke live edge | Bergantung FFmpeg segment size | +1 poll cycle (~2–4 detik) | Mendekati source |
| Beban source | Sangat minimal | Sangat minimal | Bergantung cache hit |
| Channel idle | FFmpeg berjalan | Worker tetap jalan | Tidak ada upstream fetch |
| Cocok untuk | Stream TS/RTMP langsung | Live IPTV HLS banyak viewer | Channel sedikit viewer |
| R2 Sync | Opsional | Opsional | Tidak |
| Dependensi | FFmpeg harus tersedia | Tidak ada | Tidak ada |

---

## 17. Risiko dan Mitigasi

| Risiko | Dampak | Mitigasi |
|---|---|---|
| Source memakai token yang cepat expired | Worker gagal | Custom HTTP header per channel (Referer, User-Agent, Origin) |
| Source memakai absolute segment URL | URL source bisa bocor | Rewrite semua URL di playlist (termasuk URI attribute di tag) |
| Worker crash | Channel tidak tersedia | Error dicatat di DB, channel perlu di-enable ulang; monitoring via dashboard |
| FFmpeg tidak tersedia di VPS | Transmux mode gagal | Pastikan FFmpeg terinstall; error ditampilkan jelas |
| Banyak channel aktif | RAM/CPU tinggi | `MAX_WORKERS` env membatasi worker aktif |
| Banyak viewer | Bandwidth VPS tinggi | R2 sync mengurangi beban outgoing; gunakan CDN |
| Source memblok IP VPS | Channel error | `last_error` ditampilkan di dashboard |
| SSRF via input_url | Security issue | Validasi IP, blok private network range |
| Encrypted HLS key URL bocor | Source key exposed | Rewrite `EXT-X-KEY URI` lewat signed proxy asset |
| Node agent offline | Channel di node tersebut tidak tersedia | Dashboard menampilkan status node; heartbeat timeout auto-offline |
| R2 credentials bocor | Data leak | Secret hanya disimpan terenkripsi di DB, tidak dikembalikan plain-text ke frontend |
| Session token dicuri | Unauthorized access | Session TTL 24 jam, logout menghapus token dari DB |
| Disk cache penuh | Segment tidak bisa disimpan | `LOCAL_RETENTION_SECONDS` cleanup periodik |

---

## 18. Status Implementasi

Fitur yang sudah **diimplementasi**:

- [x] Go backend dengan 3 mode: transmux, ingest, proxy
- [x] Transmux worker (FFmpeg `-c copy`)
- [x] Ingest worker (goroutine per channel, poll + disk cache)
- [x] Proxy mode dengan singleflight coalescing
- [x] Playlist rewrite (segment URL, URI attributes, master playlist)
- [x] Signed asset URL (HMAC-SHA256)
- [x] Multi-user auth (username/password, session, role: admin/viewer)
- [x] User signup (`ALLOW_SIGNUP` flag)
- [x] CRUD channel dengan ownership
- [x] Channel metrics (per window)
- [x] Live viewer tracking (60 detik window)
- [x] Share link dengan TTL
- [x] Embed page dengan video.js
- [x] Node agent (mode=node) + control plane (mode=control)
- [x] Node heartbeat, config sync, metrik report
- [x] Agent binary download via `/agent/<name>`
- [x] Cloudflare R2 integration (upload, sync, delete)
- [x] R2 config per user
- [x] Allowed origins per user (CORS)
- [x] Custom HTTP headers per channel (Referer, User-Agent, Origin)
- [x] SSRF protection (private IP validation)
- [x] Docker Compose deployment
- [x] SvelteKit frontend dengan video.js player
- [x] Settings page: nodes, R2, origins, users
- [x] Graceful shutdown
- [x] Structured JSON logging (slog)
- [x] Database migration otomatis via `store.Migrate()`

---

## 19. Definition of Done (Produksi)

Sistem dianggap production-ready jika:

1. Admin bisa membuat channel untuk stream transmux (TS/RTMP), ingest HLS, dan proxy HLS.
2. Sistem menghasilkan link `https://stream.domain.com/proxy/{slug}/index.m3u8`.
3. Link dapat diputar di VLC, video.js (embed), dan IPTV player.
4. **Transmux/ingest mode: source hanya menerima 1 koneksi per channel aktif.**
5. **Player tidak pernah memicu fetch ke source di transmux/ingest mode.**
6. Playlist dan segment tidak membocorkan URL source asli.
7. Segment cache berjalan di disk dengan cleanup otomatis.
8. Worker berjalan stabil; error tercatat di dashboard.
9. Multi-user: user dapat mendaftar, login, dan mengelola channel sendiri.
10. Node agent dapat di-deploy ke VPS berbeda dan menerima config dari control plane.
11. R2 sync berfungsi: segment yang sudah diupload ke R2 langsung menggunakan URL R2 publik.
12. Share link dan embed page berfungsi dengan TTL yang diatur.
13. CORS origin whitelist berfungsi per user.
14. Docker Compose berjalan dengan satu perintah `docker compose up -d`.
15. HTTPS via Nginx reverse proxy.

---

## 20. Pertanyaan Terbuka

1. Apakah perlu rate limiting per endpoint playback?
2. Apakah perlu batas maksimum channel per user (untuk multi-tenant)?
3. Apakah ingest/transmux worker perlu berhenti otomatis jika tidak ada viewer dalam N menit?
4. Apakah perlu notifikasi (email/webhook) saat channel error?
5. Apakah perlu dashboard analytics lebih detail (bandwidth per jam, viewer per hari)?
6. Apakah perlu support RTMP ingest langsung (tanpa URL source)?

---

## 21. Keputusan Implementasi

```
Mode default channel:   transmux (FFmpeg -c copy)
Mode relay HLS:         ingest (1 koneksi ke source, worker aktif)
Mode on-demand:         proxy (singleflight + disk cache)
Auth:                   Session-based (token di DB, TTL 24 jam)
Multi-user:             Ya — ownership per user, role admin/viewer
Node:                   Ya — control plane + node agent (binary sama, MODE env berbeda)
R2 sync:                Opsional per channel (sync_enabled + sync_delay_seconds)
CORS:                   Whitelist per user (allowed_origins table)
Signed URL:             HMAC-SHA256(signingSecret, ref)
Cache:                  Disk lokal + opsional R2
FFmpeg:                 Digunakan di transmux mode
Deployment:             Docker Compose (self-hosted) atau systemd
Frontend:               SvelteKit + video.js + @iconify/svelte (self-hosted atau Cloudflare Pages)
Database:               Neon Postgres (atau Postgres lain)
```
```
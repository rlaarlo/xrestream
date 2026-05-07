# PRD: HLS Proxy Restream Web App
**Versi:** 2.0  
**Tanggal Revisi:** 2025  
**Perubahan Utama:** Penambahan Ingest Mode (Active Relay) sebagai mode utama untuk memastikan hanya 1 koneksi ke source.

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

1. Menghasilkan link `.m3u8` relay/proxy dari input HLS source.
2. Menyembunyikan URL source asli dari client/player.
3. **Membatasi koneksi ke source hanya 1 koneksi per channel** via Ingest Mode.
4. Menghindari reencode dan proses FFmpeg untuk mode relay.
5. Menyediakan dashboard untuk membuat, mengelola, memantau, dan menonaktifkan channel.
6. Menyediakan API backend yang dapat dipakai frontend Cloudflare Pages.
7. Menyimpan konfigurasi channel dan status di Neon Postgres.

---

## 3. Mode Operasi Channel

Sistem mendukung dua mode operasi per channel. **Mode default dan yang direkomendasikan adalah Ingest Mode.**

### 3.1 Ingest Mode (Recommended — Default)

Backend secara aktif melakukan fetch ke source tanpa menunggu request dari player.

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
| Cocok untuk | Live IPTV, stream publik banyak viewer |
| Beban source | Sangat minimal |

### 3.2 Proxy Mode (On-Demand)

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
Cloudflare Pages
SvelteKit + DaisyUI
```

**Backend:**
```
Go API di VPS (Aapanel)
```

**Database:**
```
Neon Postgres
```

**Proxy/Serve:**
```
Go HLS relay handler + ingest worker
Nginx sebagai reverse proxy HTTPS
```

**Cache:**
```
In-memory cache untuk playlist
Disk cache untuk segment
Request coalescing (proxy mode)
```

---

## 5. Arsitektur Sistem

### 5.1 Ingest Mode (Primary)

```
[Source HLS Server]
       ↑
       | ← 1 koneksi per channel (Ingest Worker)
       |
[Go Backend — VPS]
   ├── Ingest Worker Pool (goroutine per channel aktif)
   │     ├── Poll playlist tiap 2 detik
   │     ├── Fetch segment baru → disk cache
   │     └── Update in-memory playlist (rewritten)
   │
   ├── Neon Postgres (config, status, metric)
   │
   └── HTTP Server
         ├── /proxy/{slug}/index.m3u8 → serve dari memory
         └── /proxy/{slug}/asset?u=...  → serve dari disk cache

[Player / IPTV]
   └──> HTTP Server → cache (tidak pernah ke source)
```

### 5.2 Proxy Mode (Secondary)

```
[Player / IPTV]
       |
       v
[Go Backend — VPS]
   ├── Cache hit → serve langsung
   └── Cache miss → fetch source (coalescing) → cache → serve

[Source HLS Server] ← hanya saat cache miss
```

### 5.3 Admin Frontend

```
Admin Browser
   ↓
Cloudflare Pages (app.domain.com)
   ↓
Go API (api.domain.com)
   ↓
Neon Postgres
```

**Domain yang disarankan:**

```
app.domain.com    → Cloudflare Pages (Admin Dashboard)
api.domain.com    → Go API VPS
stream.domain.com → HLS relay VPS
```

---

## 6. Cara Kerja Ingest Worker

### 6.1 Lifecycle Worker

```
Channel status = active
    ↓
Ingest Worker start (goroutine)
    ↓
Loop:
  1. Fetch playlist source
  2. Parse segment list
  3. Diff dengan seen segments
  4. Fetch segment baru (goroutine per segment)
  5. Rewrite playlist → simpan ke memory
  6. Sleep(pollInterval)
    ↓
Channel status = disabled / error
    ↓
Worker stop (context cancel)
```

### 6.2 Segment Fetch di Ingest Mode

```go
func (w *IngestWorker) Run(ctx context.Context, channel Channel) {
    seen := map[string]struct{}{}
    ticker := time.NewTicker(time.Duration(channel.PlaylistTTLSeconds) * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            playlist, err := w.fetchPlaylist(ctx, channel.InputURL)
            if err != nil {
                w.updateStatus(channel.ID, "source_error", err.Error())
                continue
            }

            for _, seg := range playlist.Segments {
                key := seg.ResolvedURI
                if _, ok := seen[key]; ok {
                    continue
                }
                seen[key] = struct{}{}
                go w.fetchAndCache(ctx, channel, seg)
            }

            rewritten := w.rewritePlaylist(playlist, channel)
            w.playlistCache.Set(channel.Slug, rewritten)
            w.updateStatus(channel.ID, "active", "")
        }
    }
}
```

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
GET /proxy/channel-1/asset?u={signedSegmentRef}
    ↓
Validasi signature
    ↓
Lookup file di disk cache
    ↓
Jika ada → stream ke player
Jika tidak ada (belum di-fetch worker) → 404 atau tunggu dengan timeout
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
GET /proxy/channel-1/asset?u={encodedUrl}&sig={signature}
    ↓
Validasi signature
    ↓
Decode URL segment
    ↓
Cek disk cache
    ├── Hit → stream ke player
    └── Miss → request coalescing:
          ├── Cek apakah sedang di-fetch (key lock)
          │     ├── Ya → tunggu hasil (semua waiter dapat hasil yang sama)
          │     └── Tidak → fetch dari source → cache → stream
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

| Aspek | Ingest Mode | Proxy Mode |
|---|---|---|
| Storage | Memory | Memory |
| TTL | Di-update oleh worker | 1–3 detik |
| Isi | Playlist rewritten terbaru | Playlist fetch terakhir |

### 9.2 Segment Cache

| Aspek | Ingest Mode | Proxy Mode |
|---|---|---|
| Storage | Disk lokal VPS | Disk lokal VPS |
| TTL | 60–300 detik | 30–180 detik |
| Trigger fetch | Ingest Worker | Player request (cache miss) |
| Upstream fetch | Worker saja | Player → backend → source |

### 9.3 Cache Key

```
sha256(sourceSegmentURL)
```

### 9.4 Cache Behavior

1. Segment immutable → TTL lebih panjang aman.
2. Cache harus punya batas ukuran maksimum.
3. Cache expired harus dibersihkan otomatis oleh cleanup worker.
4. Cache bisa dimatikan per channel untuk debugging (proxy mode only).

**Default awal:**

```
playlist_ttl_seconds   = 2       (proxy mode)
segment_ttl_seconds    = 120
max_cache_size_mb      = 2048
ingest_poll_seconds    = 2       (ingest mode)
segment_prefetch_ahead = 3       (fetch N segment ke depan)
```

---

## 10. Requirement Fungsional

### 10.1 Channel Management

Admin dapat:

1. Membuat channel baru.
2. Mengisi nama channel.
3. Mengisi slug channel.
4. Mengisi URL source `.m3u8`.
5. Memilih mode: **ingest** (default) atau **proxy**.
6. Mengaktifkan atau menonaktifkan channel.
7. Melihat generated proxy URL.
8. Menghapus channel.
9. Mengubah konfigurasi cache dan polling interval per channel.
10. Melihat status ingest worker (running/stopped/error).

### 10.2 Ingest Worker Management

System harus:

1. Menjalankan ingest worker saat channel mode=ingest dan status=active.
2. Menghentikan ingest worker saat channel dinonaktifkan atau dihapus.
3. Merestart worker otomatis jika crash.
4. Menampilkan status worker di dashboard.
5. Mencatat error source di database.
6. Membatasi jumlah worker aktif bersamaan sesuai kapasitas VPS.

### 10.3 Proxy Playback

System harus:

1. Menghasilkan URL `.m3u8` public/protected.
2. Me-rewrite segment URL relatif dan absolut.
3. Me-rewrite variant playlist jika source adalah master playlist.
4. Me-rewrite URL key untuk encrypted HLS jika diizinkan.
5. Memberikan header CORS agar player browser dapat memutar.
6. Mengembalikan MIME type yang benar.

**MIME type minimal:**

```
.m3u8 → application/vnd.apple.mpegurl
.ts   → video/mp2t
.m4s  → video/iso.segment
.mp4  → video/mp4
.key  → application/octet-stream
```

### 10.4 Monitoring

Dashboard harus menampilkan:

1. Status channel: active, disabled, source_error.
2. Mode channel: ingest / proxy.
3. Status ingest worker: running, stopped, error.
4. Proxy/relay URL.
5. Last request time.
6. Last source fetch status dan timestamp.
7. Cache hit ratio.
8. Upstream request count.
9. Viewer request count perkiraan.
10. Bandwidth keluar VPS perkiraan.
11. Jumlah segment di cache per channel.
12. Error terakhir dan pesan error.

### 10.5 Security

System harus:

1. Memvalidasi `input_url` hanya HTTP/HTTPS.
2. Menolak URL lokal/private network untuk mencegah SSRF.
3. Menyediakan auth untuk admin API.
4. Menyediakan optional token untuk playback URL.
5. Menyembunyikan URL source asli dari playlist output.
6. Menggunakan signed URL untuk asset/segment agar client tidak bisa membuat request bebas.
7. Membatasi ukuran response upstream.
8. Membatasi timeout fetch upstream.
9. Rate limit endpoint admin dan playback jika perlu.

**Private IP yang harus ditolak untuk source URL:**

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

**Performance target MVP:**

```
Playlist response (ingest mode) p95 < 50 ms  (serve dari memory)
Playlist response (proxy mode)  p95 < 500 ms (fetch source)
Segment cache hit p95           < 200 ms
Segment cache miss              mengikuti latency source (proxy mode only)
```

**Reliability:**

```
Backend berjalan sebagai systemd service
Auto restart jika service crash
Graceful shutdown (worker stop sebelum exit)
Structured logging
Ingest worker auto-restart jika goroutine panic
```

**Scalability MVP:**

```
Target awal: 10–50 channel aktif
Target viewer: tergantung bandwidth VPS
Satu worker per channel (ingest mode)
```

**Catatan bandwidth:**

```
Ingest mode:
  Upstream ke source = 1× bitrate per channel (bukan N×viewer)
  Downstream ke viewer = N× bitrate

Proxy mode:
  Upstream ke source = tergantung cache hit ratio
  Downstream ke viewer = N× bitrate

Cache mengurangi beban source, bukan beban outgoing ke viewer.
```

---

## 12. Database Schema

```sql
create table channels (
  id                    uuid        primary key default gen_random_uuid(),
  name                  text        not null,
  slug                  text        not null unique,
  input_url             text        not null,
  mode                  text        not null default 'ingest',
    -- 'ingest' = active relay (1 koneksi ke source)
    -- 'proxy'  = on-demand fetch (coalescing aktif)
  status                text        not null default 'active',
    -- 'active', 'disabled', 'source_error'
  worker_status         text        not null default 'stopped',
    -- 'running', 'stopped', 'error' (untuk ingest mode)
  playback_token        text,
  playlist_url          text,
  playlist_ttl_seconds  integer     not null default 2,
  segment_ttl_seconds   integer     not null default 120,
  ingest_poll_seconds   integer     not null default 2,
  cache_enabled         boolean     not null default true,
  last_request_at       timestamptz,
  last_source_fetch_at  timestamptz,
  last_source_status    integer,
  last_error            text,
  worker_started_at     timestamptz,
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now()
);

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
  created_at          timestamptz not null default now()
);

create index on channel_metrics (channel_id, window_start desc);
```

---

## 13. API Backend

### Admin API

```
GET    /health
POST   /auth/login
GET    /channels
POST   /channels
GET    /channels/{id}
PATCH  /channels/{id}
DELETE /channels/{id}
GET    /channels/{id}/metrics
POST   /channels/{id}/purge-cache
POST   /channels/{id}/worker/start   ← baru: start ingest worker manual
POST   /channels/{id}/worker/stop    ← baru: stop ingest worker manual
GET    /channels/{id}/worker/status  ← baru: status detail worker
```

### Playback API

```
GET /proxy/{slug}/index.m3u8
GET /proxy/{slug}/asset?u={encodedUrl}&sig={signature}
```

**Kenapa segment memakai signed URL:**

1. URL source asli tidak bocor ke player.
2. Client tidak bisa membuat proxy request bebas ke domain lain.
3. Backend bisa memastikan asset memang berasal dari playlist channel tersebut.

---

## 14. URL Output

```
https://stream.domain.com/proxy/{channelSlug}/index.m3u8
```

**Contoh:**

```
https://stream.domain.com/proxy/channel-1/index.m3u8
```

---

## 15. CORS dan Header Playback

```
Access-Control-Allow-Origin:   *
Access-Control-Allow-Methods:  GET, OPTIONS
Access-Control-Allow-Headers:  Range, Origin, Accept, Content-Type
Cache-Control:                 no-cache  (untuk playlist)
Cache-Control:                 public, max-age=120  (untuk segment jika aman)
```

Jika segment besar atau player mengirim Range request, backend harus mendukung atau meneruskan Range bila diperlukan.

---

## 16. Perbandingan Mode

| Aspek | Ingest Mode | Proxy Mode |
|---|---|---|
| Koneksi ke source | **1 per channel** | On-demand (cache miss) |
| Player trigger upstream | **Tidak pernah** | Ya (jika cache miss) |
| Latency ke live edge | +1 poll cycle (~2–4 detik) | Mendekati source |
| Beban source | Sangat minimal | Bergantung cache hit |
| Channel idle | Worker tetap jalan | Tidak ada upstream fetch |
| Cocok untuk | Live IPTV banyak viewer | Channel sedikit viewer |
| Kompleksitas | Lebih tinggi | Lebih rendah |

---

## 17. Risiko dan Mitigasi

| Risiko | Dampak | Mitigasi |
|---|---|---|
| Source memakai token yang cepat expired | Ingest worker gagal | Simpan header/cookie per channel, tampilkan error jelas |
| Source memakai absolute segment URL | URL source bisa bocor | Wajib parse dan rewrite semua URL di playlist |
| Ingest worker crash | Channel tidak tersedia | Auto-restart goroutine dengan backoff, alert di dashboard |
| Banyak channel aktif | RAM/CPU tinggi | Batasi max worker aktif, monitoring resource |
| Banyak viewer | Bandwidth VPS tinggi | Cache segment, monitoring bandwidth, scaling VPS |
| Source memblok IP VPS | Channel error | Tampilkan error jelas, optional upstream proxy nanti |
| SSRF via input_url | Security issue | Validasi domain/IP, blok private network |
| Encrypted HLS key URL bocor | Source key exposed | Rewrite key URI lewat signed proxy asset |
| Playlist parser tidak lengkap | Player gagal | Gunakan parser HLS yang sudah teruji |
| Disk cache penuh | Segment tidak bisa disimpan | Batas ukuran cache, eviction policy, alert |

---

## 18. Milestone Project

### Milestone 1: Foundation

Checklist:

1. Setup repo Go backend.
2. Setup koneksi Neon.
3. Buat migration `channels` dan `channel_metrics`.
4. Buat endpoint health.
5. Buat CRUD channel.
6. Deploy backend ke VPS via systemd.

### Milestone 2: Ingest Worker (Primary)

Checklist:

1. Implementasi Ingest Worker (goroutine per channel).
2. Poll playlist source setiap N detik.
3. Detect dan fetch segment baru ke disk cache.
4. Rewrite playlist di memory.
5. Worker lifecycle: start/stop/restart.
6. Endpoint `/proxy/{slug}/index.m3u8` serve dari memory.
7. Endpoint `/proxy/{slug}/asset` serve dari disk cache.
8. Test playback di VLC dan browser player.

### Milestone 3: Proxy Mode (Secondary)

Checklist:

1. Implementasi endpoint on-demand `/proxy/{slug}/index.m3u8`.
2. Fetch playlist source saat cache miss.
3. Rewrite segment URL relatif dan absolut.
4. Implementasi segment disk cache.
5. Implementasi request coalescing per segment URL.
6. Return MIME type `.m3u8` benar.
7. Test playback proxy mode.

### Milestone 4: Cache dan Reliability

Checklist:

1. Playlist memory cache dengan TTL.
2. Segment disk cache dengan cleanup worker.
3. Batas ukuran cache maksimum.
4. Cache eviction policy (LRU atau TTL-based).
5. Metric cache hit/miss.
6. Load test ingest mode 10–50 viewer bersamaan.
7. Load test proxy mode 10–50 request segment bersamaan.

### Milestone 5: Dashboard Frontend

Checklist:

1. Setup SvelteKit + DaisyUI di Cloudflare Pages.
2. Halaman login admin.
3. Halaman daftar channel dengan status worker.
4. Form create/edit channel (dengan pilihan mode: ingest/proxy).
5. Tombol copy playlist URL.
6. Tampilan status channel, worker status, dan metric dasar.
7. Tombol start/stop ingest worker manual.

### Milestone 6: Security dan Production Hardening

Checklist:

1. Admin auth (JWT atau session).
2. SSRF protection untuk input URL.
3. Signed asset URL untuk segment.
4. Optional playback token per channel.
5. Rate limiting endpoint.
6. Structured logging.
7. Nginx/Caddy HTTPS config.
8. Backup database Neon.
9. Graceful shutdown backend.

---

## 19. Definition of Done MVP

MVP dianggap selesai jika:

1. Admin bisa membuat channel dari source `.m3u8` dengan memilih mode ingest atau proxy.
2. Sistem menghasilkan link `https://stream.domain.com/proxy/{slug}/index.m3u8`.
3. Link dapat diputar di VLC dan player HLS browser.
4. **Ingest mode: source server hanya menerima 1 koneksi per channel aktif.**
5. **Player tidak pernah memicu fetch ke source di ingest mode.**
6. Playlist dan segment tidak membocorkan URL source asli.
7. Segment cache berjalan di disk.
8. Ingest worker berjalan stabil dan auto-restart jika error.
9. Proxy mode: request coalescing terbukti mencegah duplicate fetch ke source.
10. Data channel tersimpan di Neon.
11. Backend berjalan stabil di VPS via systemd.
12. Frontend berjalan di Cloudflare Pages.
13. Dashboard menampilkan status channel, status worker, dan metric dasar.

---

## 20. Pertanyaan Terbuka

1. Apakah playback URL harus public atau wajib token?
2. Apakah source HLS membutuhkan custom header, cookie, atau user-agent tertentu?
3. Berapa estimasi jumlah channel aktif bersamaan untuk sizing VPS?
4. Berapa estimasi viewer per channel?
5. Apakah cache cukup lokal VPS atau perlu object storage/CDN di masa depan?
6. Apakah ingest worker perlu berhenti otomatis jika tidak ada viewer aktif dalam N menit (untuk hemat resource)?
7. Apakah perlu fallback FFmpeg untuk input non-HLS pada versi berikutnya?

---

## 21. Keputusan Awal

```
Mode utama:         Ingest Mode (active relay, 1 koneksi ke source)
Mode sekunder:      Proxy Mode (on-demand, coalescing aktif)
Input utama:        HTTP/HTTPS .m3u8
Output utama:       .m3u8 dari domain sendiri
Backend:            Go di VPS
Database:           Neon Postgres
Frontend:           Cloudflare Pages (SvelteKit + DaisyUI)
Cache playlist:     In-memory (ingest: diupdate worker / proxy: TTL 2 detik)
Cache segment:      Disk lokal VPS
Request coalescing: Wajib untuk proxy mode, tidak diperlukan untuk ingest mode
FFmpeg:             Tidak digunakan di MVP
```
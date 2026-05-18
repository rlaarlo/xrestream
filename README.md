# Restream HLS Relay

Web app untuk membuat link `.m3u8` proxy/relay dari source HLS, dengan dashboard admin, multi-user, dan dukungan multi-VPS (node).

Output player:

```text
https://stream.domain.com/proxy/{channelSlug}/index.m3u8
```

## Fitur Utama

- **Tiga mode channel:**
  - `ingest` — backend menarik source secara aktif (1 koneksi ke source per channel), player hanya membaca cache lokal.
  - `proxy` — fetch on-demand dengan disk cache dan request coalescing.
  - `transmux` — pipeline FFmpeg untuk normalisasi/repackaging segment ke HLS lokal.
- **Disk cache** segment + **in-memory cache** playlist (per-channel TTL).
- **Signed asset URL** sehingga URL source asli tidak bocor ke player.
- **Optional playback token** per channel + endpoint `/share/...` dan `/embed/...`.
- **Multi-user** dengan role `admin` / `viewer`, session login, dan opsi self-signup (perlu approval admin).
- **Per-owner allowed origins** untuk kontrol CORS playback.
- **Per-owner Cloudflare R2 sync** opsional untuk offload segment ke object storage.
- **Multi-node (instance VPS)** — user dapat mendaftarkan node tambahan, sistem menerbitkan API key, channel dapat di-pin ke `nodeId` tertentu. Node menjalankan worker + HTTP `/proxy/...` sendiri; control plane otomatis mengarahkan `playlistUrl` ke host node.
- Endpoint distribusi binary node-agent: `GET /agent/restream-api-linux-{amd64,arm64}`.
- Metrik per-menit (playlist req, segment req, cache hit/miss, bytes, worker error) + perkiraan live viewer. Node mem-flush metrik & status worker balik ke control plane via `POST /node/report`.

> **Catatan:** R2 sync di sisi node belum aktif (node hanya pakai disk cache lokal). Channel `transmux` pada node memerlukan biner `ffmpeg` terpasang di VPS node.

## Struktur Repo

```text
backend/
  cmd/api/                   entry point (mode control plane atau node)
  internal/config/           env loader
  internal/store/            Postgres store (channels, users, sessions,
                             nodes, r2_configs, allowed_origins, metrics)
  internal/relay/            ingest worker, proxy fetch, disk cache,
                             playlist rewriter, signer, R2 client
  internal/httpapi/          HTTP handlers, auth, CORS, admin endpoints,
                             node/agent endpoints
  internal/nodeagent/        binary mode 'node' (heartbeat + config poll)
  restream-api.service       contoh systemd unit
  Dockerfile

frontend/                    SvelteKit + Tailwind + DaisyUI dashboard
deploy/nginx.conf            contoh reverse proxy
docs/PRD.md                  spesifikasi produk
docker-compose.yml           backend + frontend
```

## Arsitektur Singkat

```
[Source HLS]
     ▲
     │  1 koneksi/channel (ingest worker) atau on-demand (proxy)
     │
[Backend Go]  ──── Postgres (Neon) ──── [Admin Dashboard / SvelteKit]
     │
     ├─ /proxy/{slug}/index.m3u8     (playlist rewritten)
     ├─ /proxy/{slug}/asset?ref&sig  (segment dari disk cache / R2)
     ├─ /share/...  /embed/...       (link share/embed)
     └─ /agent/restream-api-linux-*  (download node-agent)

[Node VPS (opsional)] ── /node/heartbeat, /node/config ──▶ Control plane
```

Domain yang disarankan:

```text
app.domain.com     → frontend (Cloudflare Pages atau container nginx)
api.domain.com     → backend control plane (admin API + playback)
stream.domain.com  → alias playback (boleh sama dengan api.domain.com)
```

## Backend Lokal

```bash
cd backend
go mod tidy
go run ./cmd/api
```

Backend membaca env dari shell + file `.env` / `../.env` (loader sederhana, lihat [backend/internal/config/config.go](backend/internal/config/config.go)).

### Environment — Mode Control Plane (default)

| Var | Default | Keterangan |
|---|---|---|
| `MODE` | `control` | `control` atau `node` |
| `HTTP_ADDR` | `:3000` | Listen address |
| `DATABASE_URL` (alias: `DB_URL`, `db`) | — | Postgres URL (Neon dsb.) |
| `PUBLIC_STREAM_URL` | `http://localhost:3000` | Base URL yang dipakai untuk merangkai `playlistUrl` |
| `CACHE_DIR` | `./data/cache` | Lokasi disk cache segment |
| `SIGNING_SECRET` | `dev-change-me` | Secret untuk signed asset / playback URL |
| `ADMIN_TOKEN` | — | Service token (dipakai script/CI, login via field `token`) |
| `ADMIN_USERNAME` | `admin` | Seed admin |
| `ADMIN_PASSWORD` | fallback ke `ADMIN_TOKEN` | Password admin awal |
| `ALLOW_SIGNUP` | `true` | Izinkan self-signup (perlu approval admin) |
| `MAX_WORKERS` | `20` | Plafon worker aktif |
| `UPSTREAM_TIMEOUT_MS` | `15000` | Timeout fetch upstream |
| `PLAYBACK_TOKEN_TTL_SECONDS` | `900` | TTL token playback share |
| `LOCAL_RETENTION_SECONDS` | `60` | Retensi disk cache lokal |
| `R2_RETENTION_SECONDS` | `600` | Retensi object R2 |
| `SOURCE_ERROR_AFTER_SECONDS` | `300` | Ambang flag `source_error` |
| `ADMIN_UI_ORIGINS` / `ALLOWED_ORIGINS` | — | CSV origin frontend untuk CORS |
| `AGENT_BINARY_DIR` | `<exe-dir>/bin` | Direktori binary node-agent yang disajikan `/agent/...` |

Variabel R2 **global** (opsional fallback): `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_BUCKET`, `R2_PUBLIC_URL`. Per-user R2 dikonfigurasi via endpoint `/me/r2`.

### Login Awal

Jika `ADMIN_USERNAME` / `ADMIN_PASSWORD` belum diset, backend memakai:

```text
username: admin
password: nilai ADMIN_TOKEN
```

Untuk production, isi `ADMIN_USERNAME`, `ADMIN_PASSWORD`, dan `ADMIN_TOKEN` dengan nilai panjang dan unik. `ADMIN_TOKEN` tetap berfungsi sebagai service token (dikirim via body `{"token": "..."}` saat login).

## Frontend Lokal

```bash
cd frontend
npm install
npm run dev
```

Env:

```text
VITE_API_BASE_URL=http://localhost:3000
```

## Endpoint Ringkas

Auth & user:

```text
POST /auth/login            body: {username,password} atau {token}
POST /auth/logout
POST /auth/signup           (jika ALLOW_SIGNUP=true)
GET  /auth/me
GET  /auth/config
```

Channel (owner-scoped, admin lihat semua):

```text
GET    /channels
POST   /channels
GET    /channels/{id}
PATCH  /channels/{id}
DELETE /channels/{id}
GET    /channels/{id}/metrics
POST   /channels/{id}/purge-cache
POST   /channels/{id}/worker/start
POST   /channels/{id}/worker/stop
GET    /channels/{id}/worker/status
```

Playback:

```text
GET /proxy/{slug}/index.m3u8
GET /proxy/{slug}/asset?ref=...&sig=...
GET /share/{slug}?exp=...&sig=...
GET /embed/{slug}
```

Owner config:

```text
GET|POST          /me/nodes
GET|PATCH|DELETE  /me/nodes/{id}
GET|PUT|DELETE    /me/r2
GET|POST          /me/origins
PATCH|DELETE      /me/origins/{id}
```

Admin:

```text
GET|POST          /admin/users
PATCH|DELETE      /admin/users/{id}
GET|POST          /admin/origins
PATCH|DELETE      /admin/origins/{id}
```

Node-side (dipakai node-agent, auth `X-Node-Key: <nodeId>.<secret>`):

```text
POST /node/heartbeat
GET  /node/config         (mengirim signing_secret + daftar channel)
POST /node/report         (push status worker + metrik)
```

Distribusi binary node-agent:

```text
GET /agent/restream-api-linux-amd64
GET /agent/restream-api-linux-arm64
```

## Menjalankan Node (Mode `node`)

1. Login ke dashboard → menu **Nodes** → buat node baru, simpan API key (hanya ditampilkan sekali, format `<nodeId>.<secret>`). Isi field **Host** dengan URL publik node, misal `https://node1.domain.com` — control plane akan memakai host ini sebagai base `playlistUrl` untuk channel yang di-pin ke node tersebut.
2. Di VPS node, unduh binary:
   ```bash
   curl -fSL https://api.domain.com/agent/restream-api-linux-amd64 -o restream-api
   chmod +x restream-api
   ```
3. Jalankan dengan env:
   ```bash
   MODE=node \
   CONTROL_PLANE_URL=https://api.domain.com \
   NODE_API_KEY=<nodeId>.<secret> \
   HTTP_ADDR=:3000 \
   CACHE_DIR=/var/lib/restream/cache \
   NODE_HEARTBEAT_SECONDS=30 \
   NODE_CONFIG_POLL_SECONDS=15 \
   ./restream-api
   ```
4. Pasang reverse proxy (nginx/caddy) untuk meneruskan domain `host` node ke `HTTP_ADDR` lokal, dan pastikan domain tersebut bisa diakses publik (HTTPS direkomendasikan).
5. Buat channel di dashboard dan pilih node ini sebagai `nodeId`. Worker akan otomatis berjalan di VPS node, dan `playlistUrl` yang dihasilkan akan menunjuk ke `https://<node-host>/proxy/{slug}/index.m3u8`.

**Cara kerja singkat:**

- Node menarik `signingSecret` + daftar channel via `GET /node/config` setiap `NODE_CONFIG_POLL_SECONDS`.
- Channel baru → node otomatis `StartWorker`. Channel hilang dari assignment → `StopWorker` + purge cache lokal.
- Status worker, source status, dan metrik di-buffer di node lalu di-flush ke control plane via `POST /node/report` (setiap ~10 detik) sehingga dashboard tetap menampilkan kondisi real-time.
- Share-link / playback token yang di-mint di control plane bisa langsung diverifikasi oleh node karena memakai `SIGNING_SECRET` yang sama (didistribusikan via `/node/config`).

## Deploy VPS (Control Plane)

Build binary:

```bash
cd backend
go build -o restream-api ./cmd/api
# optional: build binary node-agent yang akan disajikan via /agent/...
GOOS=linux GOARCH=amd64 go build -o bin/restream-api-linux-amd64 ./cmd/api
GOOS=linux GOARCH=arm64 go build -o bin/restream-api-linux-arm64 ./cmd/api
```

Copy ke VPS, siapkan `.env`, pasang systemd unit dari [backend/restream-api.service](backend/restream-api.service) (default working dir `/opt/restream/backend`).

Reverse proxy: lihat contoh [deploy/nginx.conf](deploy/nginx.conf) untuk domain `api.domain.com` dan `stream.domain.com`.

## Docker Compose

Compose menyediakan service `backend` (listen `:2087`) dan `frontend` (nginx static).

```bash
cp .env.example .env   # isi variabel sesuai daftar di atas
docker compose up -d --build
```

Volume `restream-cache` dipakai untuk `CACHE_DIR=/data/cache`.

## Cloudflare Pages (Frontend)

```text
Root directory:  frontend
Build command:   npm run build
Output:          build/  (adapter static)
Env:             VITE_API_BASE_URL=https://api.domain.com
```

## Dokumentasi Lain

- [docs/PRD.md](docs/PRD.md) — spesifikasi produk, milestone, dan keputusan desain.

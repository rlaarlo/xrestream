# Restream HLS Relay

Web app untuk membuat link `.m3u8` dari source HLS dengan dua mode:

- `ingest`: backend menarik source secara aktif dan player hanya membaca cache lokal.
- `proxy`: backend fetch on-demand dengan cache dan request coalescing.

Target output:

```text
https://stream.domain.com/proxy/{channelSlug}/index.m3u8
```

## Struktur

```text
backend/   Go API, HLS relay, ingest worker, disk cache
frontend/  SvelteKit + DaisyUI dashboard untuk Cloudflare Pages
docs/      PRD dan dokumentasi produk
deploy/    contoh konfigurasi VPS
```

## Backend Lokal

```bash
cd backend
go mod tidy
go run ./cmd/api
```

Backend membaca konfigurasi dari environment. Contoh ada di `.env.example`.

Variabel penting:

```text
DATABASE_URL
PUBLIC_STREAM_URL
SIGNING_SECRET
ADMIN_TOKEN
ADMIN_USERNAME
ADMIN_PASSWORD
CACHE_DIR
```

Dashboard memakai login page. Jika `ADMIN_USERNAME` dan `ADMIN_PASSWORD` belum diset, backend memakai fallback berikut:

```text
username: admin
password: nilai ADMIN_TOKEN
```

Untuk production, isi `ADMIN_USERNAME`, `ADMIN_PASSWORD`, dan `ADMIN_TOKEN` dengan nilai panjang dan unik.

## Frontend Lokal

```bash
cd frontend
npm install
npm run dev
```

Set API URL frontend:

```text
VITE_API_BASE_URL=http://localhost:3000
```

## Deploy VPS

Build backend:

```bash
cd backend
go build -o restream-api ./cmd/api
```

Copy binary ke VPS, siapkan `.env`, lalu pasang systemd service dari `backend/restream-api.service`.

Nginx dapat memakai contoh di `deploy/nginx.conf` untuk domain:

```text
api.domain.com
stream.domain.com
```

## Cloudflare Pages

Build command:

```bash
npm run build
```

Root directory:

```text
frontend
```

Environment variable:

```text
VITE_API_BASE_URL=https://api.domain.com
```

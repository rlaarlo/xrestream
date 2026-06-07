package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
create extension if not exists pgcrypto;

create table if not exists channels (
  id                    uuid        primary key default gen_random_uuid(),
  name                  text        not null,
  slug                  text        not null unique,
  input_url             text        not null,
  mode                  text        not null default 'ingest',
  status                text        not null default 'active',
  worker_status         text        not null default 'stopped',
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
  updated_at            timestamptz not null default now(),
  constraint channels_mode_check check (mode in ('ingest', 'proxy', 'transmux')),
  constraint channels_status_check check (status in ('active', 'disabled', 'source_error')),
  constraint channels_worker_status_check check (worker_status in ('running', 'stopped', 'error'))
);

create table if not exists channel_metrics (
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

create index if not exists channel_metrics_channel_window_idx on channel_metrics (channel_id, window_start desc);
create unique index if not exists channel_metrics_channel_window_unique_idx on channel_metrics (channel_id, window_start);
`)
	if err != nil {
		return err
	}
	_, _ = s.pool.Exec(ctx, `alter table channels drop constraint if exists channels_mode_check`)
	_, err = s.pool.Exec(ctx, `alter table channels add constraint channels_mode_check check (mode in ('ingest', 'proxy', 'transmux'))`)
	if err != nil {
		return err
	}
	_, _ = s.pool.Exec(ctx, `alter table channels add column if not exists sync_enabled boolean not null default false`)
	_, err = s.pool.Exec(ctx, `alter table channels add column if not exists sync_delay_seconds integer not null default 30`)
	if err != nil {
		return err
	}
	_, _ = s.pool.Exec(ctx, `alter table channels add column if not exists http_referer text not null default ''`)
	_, _ = s.pool.Exec(ctx, `alter table channels add column if not exists http_user_agent text not null default ''`)
	_, err = s.pool.Exec(ctx, `alter table channels add column if not exists http_origin text not null default ''`)
	if err != nil {
		return err
	}
	_, _ = s.pool.Exec(ctx, `alter table channels add column if not exists playback_token_required boolean not null default true`)
	_, _ = s.pool.Exec(ctx, `alter table channels add column if not exists allowed_origins_bypass boolean not null default false`)

	_, err = s.pool.Exec(ctx, `
create table if not exists allowed_origins (
  id          uuid        primary key default gen_random_uuid(),
  origin      text        not null unique,
  label       text        not null default '',
  enabled     boolean     not null default true,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now()
);

create table if not exists users (
  id            uuid        primary key default gen_random_uuid(),
  username      text        not null unique,
  password_hash text        not null,
  role          text        not null default 'admin',
  enabled       boolean     not null default true,
  last_login_at timestamptz,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now(),
  constraint users_role_check check (role in ('admin', 'viewer'))
);

create table if not exists sessions (
  token        text        primary key,
  user_id      uuid        not null references users(id) on delete cascade,
  expires_at   timestamptz not null,
  created_at   timestamptz not null default now(),
  last_used_at timestamptz not null default now()
);

create index if not exists sessions_user_idx on sessions (user_id);
create index if not exists sessions_expires_idx on sessions (expires_at);

create table if not exists nodes (
  id            uuid        primary key default gen_random_uuid(),
  owner_id      uuid        not null references users(id) on delete cascade,
  name          text        not null,
  host          text        not null default '',
  api_key_hash  text        not null,
  status        text        not null default 'pending',
  last_seen_at  timestamptz,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now(),
  constraint nodes_status_check check (status in ('pending', 'online', 'offline'))
);
create index if not exists nodes_owner_idx on nodes (owner_id);

create table if not exists r2_configs (
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

alter table channels add column if not exists owner_id uuid references users(id) on delete set null;
alter table channels add column if not exists node_id  uuid references nodes(id) on delete set null;
create index if not exists channels_owner_idx on channels (owner_id);
create index if not exists channels_node_idx  on channels (node_id);
`)
	if err != nil {
		return err
	}
	_, _ = s.pool.Exec(ctx, `alter table allowed_origins add column if not exists owner_id uuid references users(id) on delete cascade`)
	_, _ = s.pool.Exec(ctx, `alter table allowed_origins add column if not exists channel_id uuid references channels(id) on delete cascade`)
	_, _ = s.pool.Exec(ctx, `alter table allowed_origins drop constraint if exists allowed_origins_origin_key`)
	_, _ = s.pool.Exec(ctx, `create unique index if not exists allowed_origins_owner_origin_idx on allowed_origins (coalesce(owner_id, '00000000-0000-0000-0000-000000000000'::uuid), origin)`)
	_, _ = s.pool.Exec(ctx, `create index if not exists allowed_origins_channel_idx on allowed_origins (channel_id) where channel_id is not null`)
	return nil
}

func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, baseChannelSelect+` order by created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := []Channel{}
	for rows.Next() {
		channel, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func (s *Store) ListChannelsForOwner(ctx context.Context, ownerID string) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, baseChannelSelect+` where owner_id = $1 order by created_at desc`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	return channels, rows.Err()
}

func (s *Store) ListChannelsForNode(ctx context.Context, nodeID string) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, baseChannelSelect+` where node_id = $1 and status = 'active' order by created_at asc`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	return channels, rows.Err()
}

// ActiveWorkerChannels returns channels that the *control plane* should run
// workers for: active ingest/transmux channels that are NOT pinned to a
// remote node. Node-bound channels are reconciled by the node-agent on the
// target VPS instead.
func (s *Store) ActiveWorkerChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, baseChannelSelect+` where mode in ('ingest', 'transmux') and status = 'active' and node_id is null order by created_at asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := []Channel{}
	for rows.Next() {
		channel, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func (s *Store) GetChannel(ctx context.Context, id string) (Channel, error) {
	row := s.pool.QueryRow(ctx, baseChannelSelect+` where id = $1`, id)
	return scanChannel(row)
}

func (s *Store) GetChannelBySlug(ctx context.Context, slug string) (Channel, error) {
	row := s.pool.QueryRow(ctx, baseChannelSelect+` where slug = $1`, slug)
	return scanChannel(row)
}

func (s *Store) CreateChannel(ctx context.Context, input ChannelInput, publicStreamURL string, ownerID string) (Channel, error) {
	input = normalizeInput(input)
	playlistURL := fmt.Sprintf("%s/proxy/%s/index.m3u8", strings.TrimRight(publicStreamURL, "/"), input.Slug)
	var ownerArg any
	if ownerID != "" {
		ownerArg = ownerID
	}
	row := s.pool.QueryRow(ctx, `
insert into channels (name, slug, input_url, mode, status, playlist_url, playlist_ttl_seconds, segment_ttl_seconds, ingest_poll_seconds, cache_enabled, sync_enabled, sync_delay_seconds, http_referer, http_user_agent, http_origin, playback_token_required, allowed_origins_bypass, owner_id, node_id)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
returning `+channelColumns,
		input.Name, input.Slug, input.InputURL, input.Mode, input.Status, playlistURL, input.PlaylistTTLSeconds, input.SegmentTTLSeconds, input.IngestPollSeconds, *input.CacheEnabled, *input.SyncEnabled, input.SyncDelaySeconds, input.HTTPReferer, input.HTTPUserAgent, input.HTTPOrigin, *input.PlaybackTokenRequired, *input.AllowedOriginsBypass, ownerArg, input.NodeID)
	return scanChannel(row)
}

func (s *Store) UpdateChannel(ctx context.Context, id string, input ChannelInput, publicStreamURL string) (Channel, error) {
	input = normalizeInput(input)
	playlistURL := fmt.Sprintf("%s/proxy/%s/index.m3u8", strings.TrimRight(publicStreamURL, "/"), input.Slug)
	row := s.pool.QueryRow(ctx, `
update channels set
  name = $2,
  slug = $3,
  input_url = $4,
  mode = $5,
  status = $6,
  playlist_url = $7,
  playlist_ttl_seconds = $8,
  segment_ttl_seconds = $9,
  ingest_poll_seconds = $10,
  cache_enabled = $11,
  sync_enabled = $12,
  sync_delay_seconds = $13,
  http_referer = $14,
  http_user_agent = $15,
  http_origin = $16,
  node_id = $17,
  playback_token_required = $18,
  allowed_origins_bypass = $19,
  updated_at = now()
where id = $1
returning `+channelColumns,
		id, input.Name, input.Slug, input.InputURL, input.Mode, input.Status, playlistURL, input.PlaylistTTLSeconds, input.SegmentTTLSeconds, input.IngestPollSeconds, *input.CacheEnabled, *input.SyncEnabled, input.SyncDelaySeconds, input.HTTPReferer, input.HTTPUserAgent, input.HTTPOrigin, input.NodeID, *input.PlaybackTokenRequired, *input.AllowedOriginsBypass)
	return scanChannel(row)
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `delete from channels where id = $1`, id)
	return err
}

func (s *Store) SetWorkerStatus(ctx context.Context, id, workerStatus string, lastError *string) error {
	_, err := s.pool.Exec(ctx, `
update channels
set worker_status = $2,
    last_error = $3,
    worker_started_at = case when $2 = 'running' then now() else worker_started_at end,
    updated_at = now()
where id = $1`, id, workerStatus, lastError)
	return err
}

func (s *Store) SetSourceStatus(ctx context.Context, id string, statusCode int, errMessage *string) error {
	status := "active"
	if errMessage != nil {
		status = "source_error"
	}
	_, err := s.pool.Exec(ctx, `
update channels
set status = $2,
    last_source_fetch_at = now(),
    last_source_status = $3,
    last_error = $4,
    updated_at = now()
where id = $1`, id, status, statusCode, errMessage)
	return err
}

func (s *Store) TouchRequest(ctx context.Context, id string) {
	_, _ = s.pool.Exec(ctx, `update channels set last_request_at = now() where id = $1`, id)
}

func (s *Store) IncrementMetric(ctx context.Context, channelID, field string, amount int64) {
	allowed := map[string]bool{
		"playlist_requests": true,
		"segment_requests":  true,
		"upstream_requests": true,
		"cache_hits":        true,
		"cache_misses":      true,
		"bytes_sent":        true,
		"bytes_upstream":    true,
		"worker_errors":     true,
	}
	if !allowed[field] {
		return
	}
	window := time.Now().UTC().Truncate(time.Minute)
	_, _ = s.pool.Exec(ctx, fmt.Sprintf(`
insert into channel_metrics (channel_id, window_start, %s)
values ($1, $2, $3)
on conflict (channel_id, window_start)
do update set %s = channel_metrics.%s + excluded.%s`, field, field, field, field), channelID, window, amount)
}

func (s *Store) Metrics(ctx context.Context, channelID string) (Metrics, error) {
	row := s.pool.QueryRow(ctx, `
select coalesce(sum(playlist_requests), 0), coalesce(sum(segment_requests), 0), coalesce(sum(upstream_requests), 0),
       coalesce(sum(cache_hits), 0), coalesce(sum(cache_misses), 0), coalesce(sum(bytes_sent), 0),
       coalesce(sum(bytes_upstream), 0), coalesce(sum(worker_errors), 0)
from channel_metrics
where channel_id = $1 and window_start > now() - interval '1 hour'`, channelID)
	var metrics Metrics
	err := row.Scan(&metrics.PlaylistRequests, &metrics.SegmentRequests, &metrics.UpstreamRequests, &metrics.CacheHits, &metrics.CacheMisses, &metrics.BytesSent, &metrics.BytesUpstream, &metrics.WorkerErrors)
	return metrics, err
}

const channelColumns = `id, name, slug, input_url, mode, status, worker_status, playback_token, playlist_url, playlist_ttl_seconds, segment_ttl_seconds,
	ingest_poll_seconds, cache_enabled, sync_enabled, sync_delay_seconds, http_referer, http_user_agent, http_origin, playback_token_required, allowed_origins_bypass, owner_id, node_id, last_request_at, last_source_fetch_at, last_source_status, last_error, worker_started_at, created_at, updated_at`

const baseChannelSelect = `select ` + channelColumns + ` from channels`

func scanChannel(row pgx.Row) (Channel, error) {
	var channel Channel
	err := row.Scan(
		&channel.ID,
		&channel.Name,
		&channel.Slug,
		&channel.InputURL,
		&channel.Mode,
		&channel.Status,
		&channel.WorkerStatus,
		&channel.PlaybackToken,
		&channel.PlaylistURL,
		&channel.PlaylistTTLSeconds,
		&channel.SegmentTTLSeconds,
		&channel.IngestPollSeconds,
		&channel.CacheEnabled,
		&channel.SyncEnabled,
		&channel.SyncDelaySeconds,
		&channel.HTTPReferer,
		&channel.HTTPUserAgent,
		&channel.HTTPOrigin,
		&channel.PlaybackTokenRequired,
		&channel.AllowedOriginsBypass,
		&channel.OwnerID,
		&channel.NodeID,
		&channel.LastRequestAt,
		&channel.LastSourceFetchAt,
		&channel.LastSourceStatus,
		&channel.LastError,
		&channel.WorkerStartedAt,
		&channel.CreatedAt,
		&channel.UpdatedAt,
	)
	return channel, err
}

func normalizeInput(input ChannelInput) ChannelInput {
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = strings.Trim(strings.ToLower(input.Slug), " ")
	input.InputURL = strings.TrimSpace(input.InputURL)
	input.HTTPReferer = strings.TrimSpace(input.HTTPReferer)
	input.HTTPUserAgent = strings.TrimSpace(input.HTTPUserAgent)
	input.HTTPOrigin = strings.TrimSpace(input.HTTPOrigin)
	if input.Mode == "" {
		input.Mode = "ingest"
	}
	if input.Status == "" {
		input.Status = "active"
	}
	if input.PlaylistTTLSeconds <= 0 {
		input.PlaylistTTLSeconds = 2
	}
	if input.SegmentTTLSeconds <= 0 {
		input.SegmentTTLSeconds = 120
	}
	if input.IngestPollSeconds <= 0 {
		input.IngestPollSeconds = 2
	}
	if input.CacheEnabled == nil {
		cacheEnabled := true
		input.CacheEnabled = &cacheEnabled
	}
	if input.SyncEnabled == nil {
		syncEnabled := false
		input.SyncEnabled = &syncEnabled
	}
	if input.PlaybackTokenRequired == nil {
		required := true
		input.PlaybackTokenRequired = &required
	}
	if input.AllowedOriginsBypass == nil {
		bypass := false
		input.AllowedOriginsBypass = &bypass
	}
	if input.SyncDelaySeconds <= 0 {
		input.SyncDelaySeconds = 8
	}
	return input
}

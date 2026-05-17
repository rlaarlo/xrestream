package store

import "time"

type Channel struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Slug               string     `json:"slug"`
	InputURL           string     `json:"inputUrl"`
	Mode               string     `json:"mode"`
	Status             string     `json:"status"`
	WorkerStatus       string     `json:"workerStatus"`
	PlaybackToken      *string    `json:"playbackToken,omitempty"`
	PlaylistURL        *string    `json:"playlistUrl,omitempty"`
	PlaylistTTLSeconds int        `json:"playlistTtlSeconds"`
	SegmentTTLSeconds  int        `json:"segmentTtlSeconds"`
	IngestPollSeconds  int        `json:"ingestPollSeconds"`
	CacheEnabled          bool    `json:"cacheEnabled"`
	SyncEnabled           bool    `json:"syncEnabled"`
	SyncDelaySeconds      int     `json:"syncDelaySeconds"`
	PlaybackTokenRequired bool    `json:"playbackTokenRequired"`
	HTTPReferer        string     `json:"httpReferer"`
	HTTPUserAgent      string     `json:"httpUserAgent"`
	HTTPOrigin         string     `json:"httpOrigin"`
	OwnerID            *string    `json:"ownerId,omitempty"`
	NodeID             *string    `json:"nodeId,omitempty"`
	LastRequestAt      *time.Time `json:"lastRequestAt,omitempty"`
	LastSourceFetchAt  *time.Time `json:"lastSourceFetchAt,omitempty"`
	LastSourceStatus   *int       `json:"lastSourceStatus,omitempty"`
	LastError          *string    `json:"lastError,omitempty"`
	WorkerStartedAt    *time.Time `json:"workerStartedAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type ChannelInput struct {
	Name               string `json:"name"`
	Slug               string `json:"slug"`
	InputURL           string `json:"inputUrl"`
	Mode               string `json:"mode"`
	Status             string `json:"status"`
	PlaylistTTLSeconds int    `json:"playlistTtlSeconds"`
	SegmentTTLSeconds  int    `json:"segmentTtlSeconds"`
	IngestPollSeconds  int    `json:"ingestPollSeconds"`
	CacheEnabled          *bool  `json:"cacheEnabled"`
	SyncEnabled           *bool  `json:"syncEnabled"`
	SyncDelaySeconds      int    `json:"syncDelaySeconds"`
	PlaybackTokenRequired *bool  `json:"playbackTokenRequired"`
	HTTPReferer        string  `json:"httpReferer"`
	HTTPUserAgent      string  `json:"httpUserAgent"`
	HTTPOrigin         string  `json:"httpOrigin"`
	NodeID             *string `json:"nodeId,omitempty"`
}

type Metrics struct {
	PlaylistRequests int64 `json:"playlistRequests"`
	SegmentRequests  int64 `json:"segmentRequests"`
	UpstreamRequests int64 `json:"upstreamRequests"`
	CacheHits        int64 `json:"cacheHits"`
	CacheMisses      int64 `json:"cacheMisses"`
	BytesSent        int64 `json:"bytesSent"`
	BytesUpstream    int64 `json:"bytesUpstream"`
	WorkerErrors     int64 `json:"workerErrors"`
}

type AllowedOrigin struct {
	ID        string    `json:"id"`
	OwnerID   *string   `json:"ownerId,omitempty"`
	Origin    string    `json:"origin"`
	Label     string    `json:"label"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
}

type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	Role         string     `json:"role"`
	Enabled      bool       `json:"enabled"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
}

type Node struct {
	ID         string     `json:"id"`
	OwnerID    string     `json:"ownerId"`
	Name       string     `json:"name"`
	Host       string     `json:"host"`
	Status     string     `json:"status"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

type R2Config struct {
	ID              string    `json:"id"`
	OwnerID         string    `json:"ownerId"`
	AccountID       string    `json:"accountId"`
	AccessKeyID     string    `json:"accessKeyId"`
	SecretAccessKey string    `json:"secretAccessKey,omitempty"`
	Bucket          string    `json:"bucket"`
	PublicURL       string    `json:"publicUrl"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Session struct {
	Token     string
	UserID    string
	Username  string
	Role      string
	ExpiresAt time.Time
}

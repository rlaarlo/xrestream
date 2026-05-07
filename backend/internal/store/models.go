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
	CacheEnabled       bool       `json:"cacheEnabled"`
	SyncEnabled        bool       `json:"syncEnabled"`
	SyncDelaySeconds   int        `json:"syncDelaySeconds"`
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
	CacheEnabled       *bool  `json:"cacheEnabled"`
	SyncEnabled        *bool  `json:"syncEnabled"`
	SyncDelaySeconds   int    `json:"syncDelaySeconds"`
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

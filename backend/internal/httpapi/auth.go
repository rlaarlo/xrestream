package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"sync"
	"time"

	"restream/backend/internal/store"
)

// sessionTTL is the lifetime of a login session.
const sessionTTL = 7 * 24 * time.Hour

// authCache memoises session-token lookups so admin requests don't hit the
// database on every call.
type authCache struct {
	mu  sync.Mutex
	set map[string]authCacheEntry
}

type authCacheEntry struct {
	session store.Session
	until   time.Time
}

func newAuthCache() *authCache {
	return &authCache{set: make(map[string]authCacheEntry)}
}

func (c *authCache) get(token string) (store.Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.set[token]
	if !ok || time.Now().After(e.until) {
		delete(c.set, token)
		return store.Session{}, false
	}
	return e.session, true
}

func (c *authCache) put(token string, sess store.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set[token] = authCacheEntry{session: sess, until: time.Now().Add(30 * time.Second)}
}

func (c *authCache) drop(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.set, token)
}

// originCache memoises enabled-origin sets from the database with a short
// TTL so withCORS / referer gating don't hit the DB on every request. It
// holds both the global enabled-origin set (used by CORS) and per-owner
// sets (used by per-channel referer gating).
type originCache struct {
	mu         sync.RWMutex
	set        map[string]struct{}
	expires    time.Time
	owners     map[string]ownerOriginEntry
	channels   map[string]ownerOriginEntry
	slugBypass map[string]slugBypassEntry
	ownerTTL   time.Duration
	channelTTL time.Duration
	bypassTTL  time.Duration
}

type ownerOriginEntry struct {
	set     map[string]struct{}
	expires time.Time
}

type slugBypassEntry struct {
	bypass  bool
	expires time.Time
}

func newOriginCache() *originCache {
	return &originCache{
		set:        make(map[string]struct{}),
		owners:     make(map[string]ownerOriginEntry),
		channels:   make(map[string]ownerOriginEntry),
		slugBypass: make(map[string]slugBypassEntry),
		ownerTTL:   30 * time.Second,
		channelTTL: 30 * time.Second,
		bypassTTL:  15 * time.Second,
	}
}

func (c *originCache) get(ctx context.Context, store *store.Store) map[string]struct{} {
	c.mu.RLock()
	if time.Now().Before(c.expires) {
		m := c.set
		c.mu.RUnlock()
		return m
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expires) {
		return c.set
	}
	list, err := store.ListEnabledOrigins(ctx)
	if err == nil {
		m := make(map[string]struct{}, len(list))
		for _, o := range list {
			m[strings.ToLower(o)] = struct{}{}
		}
		c.set = m
	}
	c.expires = time.Now().Add(30 * time.Second)
	return c.set
}

// getForOwner returns the cached enabled-origin set for a single owner,
// refreshing from the DB when the per-owner entry is missing or expired.
// Returns an empty (non-nil) map when the owner has no entries or on error
// so callers can treat "len == 0" as "no whitelist configured for owner".
func (c *originCache) getForOwner(ctx context.Context, store *store.Store, ownerID string) map[string]struct{} {
	if ownerID == "" {
		return map[string]struct{}{}
	}
	c.mu.RLock()
	if e, ok := c.owners[ownerID]; ok && time.Now().Before(e.expires) {
		m := e.set
		c.mu.RUnlock()
		return m
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.owners[ownerID]; ok && time.Now().Before(e.expires) {
		return e.set
	}
	list, err := store.ListEnabledOriginsForOwner(ctx, ownerID)
	m := map[string]struct{}{}
	if err == nil {
		for _, o := range list {
			m[strings.ToLower(o)] = struct{}{}
		}
	}
	c.owners[ownerID] = ownerOriginEntry{set: m, expires: time.Now().Add(c.ownerTTL)}
	return m
}

func (c *originCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expires = time.Time{}
	c.owners = make(map[string]ownerOriginEntry)
	c.channels = make(map[string]ownerOriginEntry)
}

// invalidateSlugBypass drops the cached allowed-origins bypass flag for the
// given slug (or all slugs when slug == ""). Called after channel writes so
// toggle changes take effect immediately instead of waiting for the TTL.
func (c *originCache) invalidateSlugBypass(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slug == "" {
		c.slugBypass = make(map[string]slugBypassEntry)
		return
	}
	delete(c.slugBypass, slug)
}

// channelBypass reports whether the channel identified by slug has the
// allowed_origins enforcement bypass enabled. The result is cached for a
// short TTL so hot-path CORS / referer checks don't hit the DB on every
// stream request. Returns false on lookup error (fail-closed for the
// bypass-grant decision).
func (c *originCache) channelBypass(ctx context.Context, st *store.Store, slug string) bool {
	if slug == "" || st == nil {
		return false
	}
	c.mu.RLock()
	if e, ok := c.slugBypass[slug]; ok && time.Now().Before(e.expires) {
		b := e.bypass
		c.mu.RUnlock()
		return b
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.slugBypass[slug]; ok && time.Now().Before(e.expires) {
		return e.bypass
	}
	bypass := false
	if ch, err := st.GetChannelBySlug(ctx, slug); err == nil {
		bypass = ch.AllowedOriginsBypass
	}
	c.slugBypass[slug] = slugBypassEntry{bypass: bypass, expires: time.Now().Add(c.bypassTTL)}
	return bypass
}

// getForChannel returns the cached effective enabled-origin set for a single
// channel (channel-scoped entries unioned with owner-wide ones). Used by
// per-channel referer gating. Returns an empty (non-nil) map when there is
// no whitelist or on error so callers treat "len == 0" as "open".
func (c *originCache) getForChannel(ctx context.Context, st *store.Store, channelID, ownerID string) map[string]struct{} {
	if channelID == "" {
		return c.getForOwner(ctx, st, ownerID)
	}
	c.mu.RLock()
	if e, ok := c.channels[channelID]; ok && time.Now().Before(e.expires) {
		m := e.set
		c.mu.RUnlock()
		return m
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.channels[channelID]; ok && time.Now().Before(e.expires) {
		return e.set
	}
	list, err := st.ListEnabledOriginsForChannel(ctx, channelID, ownerID)
	m := map[string]struct{}{}
	if err == nil {
		for _, o := range list {
			m[strings.ToLower(o)] = struct{}{}
		}
	}
	c.channels[channelID] = ownerOriginEntry{set: m, expires: time.Now().Add(c.channelTTL)}
	return m
}

func newSessionToken() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

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

// originCache memoises the enabled-origin set from the database with a short
// TTL so withCORS doesn't hit the DB on every preflight.
type originCache struct {
	mu      sync.RWMutex
	set     map[string]struct{}
	expires time.Time
}

func newOriginCache() *originCache {
	return &originCache{set: make(map[string]struct{})}
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

func (c *originCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expires = time.Time{}
}

func newSessionToken() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

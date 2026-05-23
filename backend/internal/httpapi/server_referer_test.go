package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"restream/backend/internal/config"
)

// newTestServer builds a *Server with just enough wiring to exercise the
// referer / origin gating helpers. It deliberately leaves `store` and
// `relay` nil — tests must pre-warm the originCache so no DB call is made.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		cfg: config.Config{
			PublicStreamURL: "https://stream.example",
			AdminToken:      "admin-secret",
		},
		auth:    newAuthCache(),
		origins: newOriginCache(),
		rootCtx: context.Background(),
	}
	// Pre-warm the global cache to an explicit empty state with a far-future
	// expiry so refererAllowed never falls through to the (nil) store. Tests
	// that want a populated whitelist call setGlobal/setOwner to overwrite.
	s.origins.expires = time.Now().Add(time.Hour)
	return s
}

func setGlobal(s *Server, origins ...string) {
	s.origins.mu.Lock()
	defer s.origins.mu.Unlock()
	m := map[string]struct{}{}
	for _, o := range origins {
		m[strings.ToLower(o)] = struct{}{}
	}
	s.origins.set = m
	s.origins.expires = time.Now().Add(time.Hour)
}

func setOwner(s *Server, ownerID string, origins ...string) {
	s.origins.mu.Lock()
	defer s.origins.mu.Unlock()
	m := map[string]struct{}{}
	for _, o := range origins {
		m[strings.ToLower(o)] = struct{}{}
	}
	s.origins.owners[ownerID] = ownerOriginEntry{set: m, expires: time.Now().Add(time.Hour)}
}

func setChannel(s *Server, channelID string, origins ...string) {
	s.origins.mu.Lock()
	defer s.origins.mu.Unlock()
	m := map[string]struct{}{}
	for _, o := range origins {
		m[strings.ToLower(o)] = struct{}{}
	}
	s.origins.channels[channelID] = ownerOriginEntry{set: m, expires: time.Now().Add(time.Hour)}
}

func reqWithReferer(referer string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/proxy/x/index.m3u8", nil)
	if referer != "" {
		r.Header.Set("Referer", referer)
	}
	return r
}

func TestOriginOf(t *testing.T) {
	cases := map[string]string{
		"":                                      "",
		"https://example.com":                   "https://example.com",
		"HTTPS://Example.COM/page?x=1":          "https://example.com",
		"http://news.com/article":               "http://news.com",
		"not-a-url":                             "",
		"://broken":                             "",
		"https://utama.com:8443/foo":            "https://utama.com:8443",
	}
	for in, want := range cases {
		if got := originOf(in); got != want {
			t.Errorf("originOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRefererAllowed_EmptyWhitelistOpen(t *testing.T) {
	s := newTestServer(t)
	// Whitelist empty → open by default (back-compat).
	if !s.refererAllowed(reqWithReferer(""), "", "") {
		t.Fatal("expected open when global whitelist empty and no referer")
	}
	setOwner(s, "owner-x") // pre-warm empty owner entry
	if !s.refererAllowed(reqWithReferer("https://anywhere.com"), "", "owner-x") {
		t.Fatal("expected open when owner whitelist empty")
	}
}

func TestRefererAllowed_GlobalWhitelist(t *testing.T) {
	s := newTestServer(t)
	setGlobal(s, "https://utama.com")

	if s.refererAllowed(reqWithReferer(""), "", "") {
		t.Fatal("missing referer should be denied when whitelist set")
	}
	if s.refererAllowed(reqWithReferer("https://other.com"), "", "") {
		t.Fatal("non-matching referer should be denied")
	}
	if !s.refererAllowed(reqWithReferer("https://utama.com/post/1"), "", "") {
		t.Fatal("matching referer should be allowed")
	}
	if !s.refererAllowed(reqWithReferer("HTTPS://Utama.com/post/2"), "", "") {
		t.Fatal("matching referer (case-insensitive) should be allowed")
	}
	// Self-origin (hls.js fetching /share from /embed) always allowed.
	if !s.refererAllowed(reqWithReferer("https://stream.example/embed/x"), "", "") {
		t.Fatal("self origin should be allowed")
	}
}

func TestRefererAllowed_PerOwnerIsolation(t *testing.T) {
	s := newTestServer(t)
	setOwner(s, "owner-A", "https://news-a.com")
	setOwner(s, "owner-B", "https://news-b.com")

	if !s.refererAllowed(reqWithReferer("https://news-a.com/x"), "", "owner-A") {
		t.Fatal("owner-A's referer should pass for owner-A")
	}
	if s.refererAllowed(reqWithReferer("https://news-a.com/x"), "", "owner-B") {
		t.Fatal("owner-A's referer must NOT unlock owner-B's stream")
	}
	if !s.refererAllowed(reqWithReferer("https://news-b.com/y"), "", "owner-B") {
		t.Fatal("owner-B's referer should pass for owner-B")
	}
}

func TestRefererAllowed_PerChannelIsolation(t *testing.T) {
	s := newTestServer(t)
	// Two channels under the same owner, each with its own whitelist.
	setChannel(s, "chan-A", "https://news-a.com")
	setChannel(s, "chan-B", "https://news-b.com")

	if !s.refererAllowed(reqWithReferer("https://news-a.com/x"), "chan-A", "owner-1") {
		t.Fatal("chan-A's referer should pass for chan-A")
	}
	if s.refererAllowed(reqWithReferer("https://news-a.com/x"), "chan-B", "owner-1") {
		t.Fatal("chan-A's referer must NOT unlock chan-B")
	}
	if !s.refererAllowed(reqWithReferer("https://news-b.com/y"), "chan-B", "owner-1") {
		t.Fatal("chan-B's referer should pass for chan-B")
	}
}

func TestRefererGate_AdminBypass(t *testing.T) {
	s := newTestServer(t)
	setGlobal(s, "https://utama.com") // would normally block

	r := reqWithReferer("https://attacker.com")
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()

	if !s.refererGate(w, r) {
		t.Fatalf("admin bearer should bypass; got status %d", w.Code)
	}
}

func TestRefererGate_DeniesWithoutSession(t *testing.T) {
	s := newTestServer(t)
	setGlobal(s, "https://utama.com")

	r := reqWithReferer("https://evil.com")
	w := httptest.NewRecorder()

	if s.refererGate(w, r) {
		t.Fatal("expected gate to deny")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

func TestFrameAncestorsCSP(t *testing.T) {
	s := newTestServer(t)
	if got := s.frameAncestorsCSP("", ""); got != "" {
		t.Fatalf("empty whitelist should yield empty CSP, got %q", got)
	}
	setOwner(s, "owner-1", "https://utama.com")
	csp := s.frameAncestorsCSP("", "owner-1")
	if !strings.HasPrefix(csp, "frame-ancestors ") {
		t.Fatalf("bad CSP prefix: %q", csp)
	}
	if !strings.Contains(csp, "'self'") || !strings.Contains(csp, "https://utama.com") {
		t.Fatalf("CSP missing expected sources: %q", csp)
	}
	// owner-2 has no entries → empty
	setOwner(s, "owner-2") // pre-warm empty owner entry
	if got := s.frameAncestorsCSP("", "owner-2"); got != "" {
		t.Fatalf("unknown owner should yield empty CSP, got %q", got)
	}
}

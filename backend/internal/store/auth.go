package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("not found")

// --- Allowed origins ---------------------------------------------------------

func (s *Store) ListAllowedOrigins(ctx context.Context) ([]AllowedOrigin, error) {
	rows, err := s.pool.Query(ctx, `select id, owner_id, channel_id, origin, label, enabled, created_at from allowed_origins order by created_at asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AllowedOrigin{}
	for rows.Next() {
		var o AllowedOrigin
		if err := rows.Scan(&o.ID, &o.OwnerID, &o.ChannelID, &o.Origin, &o.Label, &o.Enabled, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) ListAllowedOriginsForOwner(ctx context.Context, ownerID string) ([]AllowedOrigin, error) {
	rows, err := s.pool.Query(ctx, `select id, owner_id, channel_id, origin, label, enabled, created_at from allowed_origins where owner_id = $1 order by created_at asc`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AllowedOrigin{}
	for rows.Next() {
		var o AllowedOrigin
		if err := rows.Scan(&o.ID, &o.OwnerID, &o.ChannelID, &o.Origin, &o.Label, &o.Enabled, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) ListEnabledOrigins(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select origin from allowed_origins where enabled = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimRight(strings.TrimSpace(o), "/"))
	}
	return out, rows.Err()
}

// ListEnabledOriginsForOwner returns enabled allowed-origins belonging to a
// single owner. Used by per-channel referer / frame-ancestors gating so one
// owner's whitelist does not unlock another owner's streams.
func (s *Store) ListEnabledOriginsForOwner(ctx context.Context, ownerID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select origin from allowed_origins where enabled = true and owner_id = $1`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimRight(strings.TrimSpace(o), "/"))
	}
	return out, rows.Err()
}

// ListEnabledOriginsForChannel returns the effective enabled allowed-origins
// for a specific channel. The effective set is the union of:
//   - origins scoped to this channel (channel_id = $1), and
//   - owner-wide origins (channel_id is null and owner_id = $2).
// This lets owners configure per-channel whitelists while keeping shared
// owner-wide entries (the legacy behaviour) working.
func (s *Store) ListEnabledOriginsForChannel(ctx context.Context, channelID, ownerID string) ([]string, error) {
	var ownerArg any
	if ownerID != "" {
		ownerArg = ownerID
	}
	rows, err := s.pool.Query(ctx, `
select origin from allowed_origins
where enabled = true
  and (channel_id = $1 or (channel_id is null and owner_id is not distinct from $2))`, channelID, ownerArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimRight(strings.TrimSpace(o), "/"))
	}
	return out, rows.Err()
}

func (s *Store) CreateAllowedOrigin(ctx context.Context, ownerID, origin, label string) (AllowedOrigin, error) {
	return s.CreateAllowedOriginScoped(ctx, ownerID, "", origin, label)
}

// CreateAllowedOriginScoped creates an allowed-origin optionally scoped to a
// single channel. Pass channelID = "" for an owner-wide entry (legacy).
func (s *Store) CreateAllowedOriginScoped(ctx context.Context, ownerID, channelID, origin, label string) (AllowedOrigin, error) {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return AllowedOrigin{}, errors.New("origin is required")
	}
	var ownerArg any
	if ownerID != "" {
		ownerArg = ownerID
	}
	var channelArg any
	if channelID != "" {
		channelArg = channelID
	}
	var o AllowedOrigin
	row := s.pool.QueryRow(ctx, `
insert into allowed_origins (owner_id, channel_id, origin, label) values ($1, $2, $3, $4)
returning id, owner_id, channel_id, origin, label, enabled, created_at`, ownerArg, channelArg, origin, strings.TrimSpace(label))
	err := row.Scan(&o.ID, &o.OwnerID, &o.ChannelID, &o.Origin, &o.Label, &o.Enabled, &o.CreatedAt)
	return o, err
}

func (s *Store) UpdateAllowedOrigin(ctx context.Context, id string, label string, enabled bool) (AllowedOrigin, error) {
	var o AllowedOrigin
	row := s.pool.QueryRow(ctx, `
update allowed_origins set label = $2, enabled = $3, updated_at = now()
where id = $1
returning id, owner_id, channel_id, origin, label, enabled, created_at`, id, strings.TrimSpace(label), enabled)
	err := row.Scan(&o.ID, &o.OwnerID, &o.ChannelID, &o.Origin, &o.Label, &o.Enabled, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

func (s *Store) UpdateAllowedOriginForOwner(ctx context.Context, ownerID, id, label string, enabled bool) (AllowedOrigin, error) {
	var o AllowedOrigin
	row := s.pool.QueryRow(ctx, `
update allowed_origins set label = $3, enabled = $4, updated_at = now()
where id = $1 and owner_id = $2
returning id, owner_id, channel_id, origin, label, enabled, created_at`, id, ownerID, strings.TrimSpace(label), enabled)
	err := row.Scan(&o.ID, &o.OwnerID, &o.ChannelID, &o.Origin, &o.Label, &o.Enabled, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

func (s *Store) DeleteAllowedOrigin(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `delete from allowed_origins where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAllowedOriginForOwner(ctx context.Context, ownerID, id string) error {
	tag, err := s.pool.Exec(ctx, `delete from allowed_origins where id = $1 and owner_id = $2`, id, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Users -------------------------------------------------------------------

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `select id, username, role, enabled, created_at, updated_at, last_login_at from users order by created_at asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `select count(*) from users`).Scan(&n)
	return n, err
}

func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	row := s.pool.QueryRow(ctx, `select id, username, role, enabled, created_at, updated_at, last_login_at from users where id = $1`, id)
	err := row.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, string, error) {
	var u User
	var hash string
	row := s.pool.QueryRow(ctx, `select id, username, role, enabled, created_at, updated_at, last_login_at, password_hash from users where lower(username) = lower($1)`, strings.TrimSpace(username))
	err := row.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, "", ErrNotFound
	}
	return u, hash, err
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string, enabled bool) (User, error) {
	if role == "" {
		role = "admin"
	}
	var u User
	row := s.pool.QueryRow(ctx, `
insert into users (username, password_hash, role, enabled) values ($1, $2, $3, $4)
returning id, username, role, enabled, created_at, updated_at, last_login_at`,
		strings.TrimSpace(username), passwordHash, role, enabled)
	err := row.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	return u, err
}

func (s *Store) UpdateUser(ctx context.Context, id, role string, enabled bool) (User, error) {
	if role == "" {
		role = "admin"
	}
	var u User
	row := s.pool.QueryRow(ctx, `
update users set role = $2, enabled = $3, updated_at = now()
where id = $1
returning id, username, role, enabled, created_at, updated_at, last_login_at`, id, role, enabled)
	err := row.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) SetUserPassword(ctx context.Context, id, passwordHash string) error {
	tag, err := s.pool.Exec(ctx, `update users set password_hash = $2, updated_at = now() where id = $1`, id, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// invalidate all sessions when password changes
	_, _ = s.pool.Exec(ctx, `delete from sessions where user_id = $1`, id)
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `delete from users where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchUserLogin(ctx context.Context, id string) {
	_, _ = s.pool.Exec(ctx, `update users set last_login_at = now() where id = $1`, id)
}

// --- Sessions ----------------------------------------------------------------

func (s *Store) CreateSession(ctx context.Context, userID, token string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `insert into sessions (token, user_id, expires_at) values ($1, $2, $3)`, token, userID, expiresAt)
	return err
}

func (s *Store) LookupSession(ctx context.Context, token string) (Session, error) {
	var sess Session
	row := s.pool.QueryRow(ctx, `
select s.token, s.user_id, s.expires_at, u.username, u.role
from sessions s
join users u on u.id = s.user_id
where s.token = $1 and u.enabled = true and s.expires_at > now()`, token)
	err := row.Scan(&sess.Token, &sess.UserID, &sess.ExpiresAt, &sess.Username, &sess.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return sess, ErrNotFound
	}
	if err == nil {
		_, _ = s.pool.Exec(ctx, `update sessions set last_used_at = now() where token = $1`, token)
	}
	return sess, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where token = $1`, token)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where user_id = $1`, userID)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) {
	_, _ = s.pool.Exec(ctx, `delete from sessions where expires_at < now()`)
}

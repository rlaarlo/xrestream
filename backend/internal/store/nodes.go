package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- Nodes -------------------------------------------------------------------

func (s *Store) ListNodes(ctx context.Context, ownerID string) ([]Node, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if ownerID == "" {
		rows, err = s.pool.Query(ctx, `select id, owner_id, name, host, status, last_seen_at, created_at, updated_at from nodes order by created_at desc`)
	} else {
		rows, err = s.pool.Query(ctx, `select id, owner_id, name, host, status, last_seen_at, created_at, updated_at from nodes where owner_id = $1 order by created_at desc`, ownerID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.OwnerID, &n.Name, &n.Host, &n.Status, &n.LastSeenAt, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNode(ctx context.Context, id string) (Node, error) {
	var n Node
	row := s.pool.QueryRow(ctx, `select id, owner_id, name, host, status, last_seen_at, created_at, updated_at from nodes where id = $1`, id)
	err := row.Scan(&n.ID, &n.OwnerID, &n.Name, &n.Host, &n.Status, &n.LastSeenAt, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// GetNodeWithKeyHash returns the node along with its api_key_hash (for auth).
func (s *Store) GetNodeWithKeyHash(ctx context.Context, id string) (Node, string, error) {
	var n Node
	var hash string
	row := s.pool.QueryRow(ctx, `select id, owner_id, name, host, status, last_seen_at, created_at, updated_at, api_key_hash from nodes where id = $1`, id)
	err := row.Scan(&n.ID, &n.OwnerID, &n.Name, &n.Host, &n.Status, &n.LastSeenAt, &n.CreatedAt, &n.UpdatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, "", ErrNotFound
	}
	return n, hash, err
}

func (s *Store) CreateNode(ctx context.Context, ownerID, name, host, apiKeyHash string) (Node, error) {
	var n Node
	row := s.pool.QueryRow(ctx, `
insert into nodes (owner_id, name, host, api_key_hash) values ($1, $2, $3, $4)
returning id, owner_id, name, host, status, last_seen_at, created_at, updated_at`,
		ownerID, strings.TrimSpace(name), strings.TrimSpace(host), apiKeyHash)
	err := row.Scan(&n.ID, &n.OwnerID, &n.Name, &n.Host, &n.Status, &n.LastSeenAt, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

func (s *Store) UpdateNode(ctx context.Context, id, name, host string) (Node, error) {
	var n Node
	row := s.pool.QueryRow(ctx, `
update nodes set name = $2, host = $3, updated_at = now()
where id = $1
returning id, owner_id, name, host, status, last_seen_at, created_at, updated_at`,
		id, strings.TrimSpace(name), strings.TrimSpace(host))
	err := row.Scan(&n.ID, &n.OwnerID, &n.Name, &n.Host, &n.Status, &n.LastSeenAt, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `delete from nodes where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchNodeHeartbeat(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `update nodes set status = 'online', last_seen_at = now(), updated_at = now() where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkStaleNodesOffline flips any 'online' node to 'offline' when its
// last_seen_at is older than the cutoff. Intended to be called periodically.
func (s *Store) MarkStaleNodesOffline(ctx context.Context, cutoff time.Duration) {
	_, _ = s.pool.Exec(ctx, `update nodes set status = 'offline', updated_at = now() where status = 'online' and (last_seen_at is null or last_seen_at < now() - ($1 || ' seconds')::interval)`, int(cutoff.Seconds()))
}

// --- R2 configs --------------------------------------------------------------

func (s *Store) GetR2Config(ctx context.Context, ownerID string) (R2Config, error) {
	var c R2Config
	row := s.pool.QueryRow(ctx, `select id, owner_id, account_id, access_key_id, secret_access_key, bucket, public_url, created_at, updated_at from r2_configs where owner_id = $1`, ownerID)
	err := row.Scan(&c.ID, &c.OwnerID, &c.AccountID, &c.AccessKeyID, &c.SecretAccessKey, &c.Bucket, &c.PublicURL, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) UpsertR2Config(ctx context.Context, ownerID, accountID, accessKey, secret, bucket, publicURL string) (R2Config, error) {
	var c R2Config
	row := s.pool.QueryRow(ctx, `
insert into r2_configs (owner_id, account_id, access_key_id, secret_access_key, bucket, public_url)
values ($1, $2, $3, $4, $5, $6)
on conflict (owner_id) do update set
  account_id = excluded.account_id,
  access_key_id = excluded.access_key_id,
  secret_access_key = excluded.secret_access_key,
  bucket = excluded.bucket,
  public_url = excluded.public_url,
  updated_at = now()
returning id, owner_id, account_id, access_key_id, secret_access_key, bucket, public_url, created_at, updated_at`,
		ownerID, strings.TrimSpace(accountID), strings.TrimSpace(accessKey), secret, strings.TrimSpace(bucket), strings.TrimRight(strings.TrimSpace(publicURL), "/"))
	err := row.Scan(&c.ID, &c.OwnerID, &c.AccountID, &c.AccessKeyID, &c.SecretAccessKey, &c.Bucket, &c.PublicURL, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) DeleteR2Config(ctx context.Context, ownerID string) error {
	tag, err := s.pool.Exec(ctx, `delete from r2_configs where owner_id = $1`, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

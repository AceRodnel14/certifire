// Package store is CertiFire's Postgres layer. It holds ciphertext and plain
// metadata only; all key handling happens in callers.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value BYTEA NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id                  BIGSERIAL PRIMARY KEY,
    username            TEXT NOT NULL,
    is_admin            BOOLEAN NOT NULL DEFAULT false,
    auth_salt           BYTEA NOT NULL,
    auth_hash           BYTEA NOT NULL,
    kek_salt            BYTEA NOT NULL,
    wrapped_dek         BYTEA NOT NULL,
    escrow_dek          BYTEA NOT NULL,
    theme_mode          TEXT NOT NULL DEFAULT 'auto',
    theme_hue           SMALLINT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    password_changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS users_username_idx ON users (lower(username));

CREATE TABLE IF NOT EXISTS sessions (
    token_hash  BYTEA PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    wrapped_dek BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);

CREATE TABLE IF NOT EXISTS certs (
    id         UUID PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    hosts      TEXT[] NOT NULL,
    bits       INT NOT NULL,
    days       INT NOT NULL,
    subject    JSONB NOT NULL,
    cnf        TEXT NOT NULL,
    cert_pem   TEXT NOT NULL,
    key_enc    BYTEA NOT NULL,
    not_before TIMESTAMPTZ NOT NULL,
    not_after  TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    renewed_by UUID REFERENCES certs(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS certs_user_idx ON certs (user_id, created_at);

CREATE TABLE IF NOT EXISTS webhooks (
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('discord', 'teams')),
    url           TEXT NOT NULL,
    broken_at     TIMESTAMPTZ,
    broken_reason TEXT,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind)
);

CREATE TABLE IF NOT EXISTS reminders_sent (
    cert_id UUID NOT NULL REFERENCES certs(id) ON DELETE CASCADE,
    kind    TEXT NOT NULL,
    mark    TEXT NOT NULL,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (cert_id, kind, mark)
);
`

// Errors callers map to HTTP statuses.
var (
	ErrNotFound       = errors.New("not found")
	ErrExists         = errors.New("already exists")
	ErrAlreadySetUp   = errors.New("already set up")
	ErrAlreadyRenewed = errors.New("already renewed")
)

// Store wraps the connection pool.
type Store struct{ pool *pgxpool.Pool }

// Open connects, pings and applies the schema.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks the database connection.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// --- meta and setup ---

// Meta keys.
const (
	MetaEscrowPub  = "escrow_pub"
	MetaEscrowPriv = "escrow_priv"
	MetaMasterSalt = "master_salt"
)

// GetMeta returns a meta value or ErrNotFound.
func (s *Store) GetMeta(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM meta WHERE key = $1`, key).Scan(&v)
	return v, notFound(err)
}

// IsSetUp reports whether setup has run.
func (s *Store) IsSetUp(ctx context.Context) (bool, error) {
	_, err := s.GetMeta(ctx, MetaEscrowPub)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Setup stores the escrow material and creates the admin user atomically.
// It returns ErrAlreadySetUp if setup ran before.
func (s *Store) Setup(ctx context.Context, meta map[string][]byte, admin *User) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7243002)`); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM meta WHERE key = $1`, MetaEscrowPub).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrAlreadySetUp
	}
	for k, v := range meta {
		if _, err := tx.Exec(ctx, `INSERT INTO meta (key, value) VALUES ($1, $2)`, k, v); err != nil {
			return err
		}
	}
	if err := insertUser(ctx, tx, admin); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- users ---

// Theme is a user's appearance preference. A nil Hue is the default accent.
type Theme struct {
	Mode string `json:"mode"`
	Hue  *int16 `json:"hue"`
}

// User is one account row.
type User struct {
	ID         int64
	Username   string
	IsAdmin    bool
	AuthSalt   []byte
	AuthHash   []byte
	KEKSalt    []byte
	WrappedDEK []byte
	EscrowDEK  []byte
	Theme      Theme
}

func insertUser(ctx context.Context, q querier, u *User) error {
	err := q.QueryRow(ctx,
		`INSERT INTO users (username, is_admin, auth_salt, auth_hash, kek_salt, wrapped_dek, escrow_dek)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, theme_mode`,
		u.Username, u.IsAdmin, u.AuthSalt, u.AuthHash, u.KEKSalt, u.WrappedDEK, u.EscrowDEK,
	).Scan(&u.ID, &u.Theme.Mode)
	if isUnique(err) {
		return ErrExists
	}
	return err
}

// CreateUser inserts u and sets its ID; ErrExists if the name is taken.
func (s *Store) CreateUser(ctx context.Context, u *User) error { return insertUser(ctx, s.pool, u) }

const userCols = `id, username, is_admin, auth_salt, auth_hash, kek_salt, wrapped_dek, escrow_dek, theme_mode, theme_hue`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.AuthSalt, &u.AuthHash, &u.KEKSalt, &u.WrappedDEK, &u.EscrowDEK, &u.Theme.Mode, &u.Theme.Hue)
	if err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

// UserByName finds a user case-insensitively.
func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(username) = lower($1)`, name))
}

// AllUsers returns every user ordered by lower(username).
func (s *Store) AllUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY lower(username), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserByID finds a user by id.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

// SavePassword stores the re-wrapped password material and revokes the user's
// sessions except keepTokenHash (nil revokes all), in one transaction.
func (s *Store) SavePassword(ctx context.Context, u *User, keepTokenHash []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`UPDATE users SET auth_salt = $2, auth_hash = $3, kek_salt = $4, wrapped_dek = $5, password_changed_at = now() WHERE id = $1`,
		u.ID, u.AuthSalt, u.AuthHash, u.KEKSalt, u.WrappedDEK); err != nil {
		return err
	}
	if keepTokenHash == nil {
		keepTokenHash = []byte{}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, u.ID, keepTokenHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetTheme stores a user's theme.
func (s *Store) SetTheme(ctx context.Context, userID int64, t Theme) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET theme_mode = $2, theme_hue = $3 WHERE id = $1`, userID, t.Mode, t.Hue)
	return err
}

// UserSummary is one row of the admin list.
type UserSummary struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	IsAdmin   bool      `json:"isAdmin"`
	CreatedAt time.Time `json:"createdAt"`
	CertCount int       `json:"certCount"`
}

// ListUsers returns all users with their certificate counts.
func (s *Store) ListUsers(ctx context.Context) ([]UserSummary, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT u.id, u.username, u.is_admin, u.created_at, count(c.id)
		 FROM users u LEFT JOIN certs c ON c.user_id = u.id
		 GROUP BY u.id ORDER BY u.is_admin DESC, lower(u.username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserSummary{}
	for rows.Next() {
		var u UserSummary
		if err := rows.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.CreatedAt, &u.CertCount); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- sessions ---

// Session is a stored session joined with its user.
type Session struct {
	UserID     int64
	Username   string
	IsAdmin    bool
	Theme      Theme
	WrappedDEK []byte
	ExpiresAt  time.Time
}

// CreateSession stores a session.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID int64, wrappedDEK []byte, expires time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, wrapped_dek, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, userID, wrappedDEK, expires)
	return err
}

// GetSession returns an unexpired session or ErrNotFound.
func (s *Store) GetSession(ctx context.Context, tokenHash []byte) (*Session, error) {
	var x Session
	err := s.pool.QueryRow(ctx,
		`SELECT s.user_id, u.username, u.is_admin, u.theme_mode, u.theme_hue, s.wrapped_dek, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash,
	).Scan(&x.UserID, &x.Username, &x.IsAdmin, &x.Theme.Mode, &x.Theme.Hue, &x.WrappedDEK, &x.ExpiresAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &x, nil
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// ReapSessions deletes expired sessions.
func (s *Store) ReapSessions(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return err
}

// --- webhooks ---

// Webhook is one stored webhook.
type Webhook struct {
	Kind         string
	URL          string
	BrokenAt     *time.Time
	BrokenReason *string
	UpdatedAt    time.Time
}

// SetWebhook stores (replacing) a webhook and clears its broken status.
func (s *Store) SetWebhook(ctx context.Context, userID int64, kind, url string) (*Webhook, error) {
	w := Webhook{Kind: kind}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO webhooks (user_id, kind, url) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, kind) DO UPDATE
		 SET url = EXCLUDED.url, broken_at = NULL, broken_reason = NULL, updated_at = now()
		 RETURNING url, broken_at, broken_reason, updated_at`, userID, kind, url,
	).Scan(&w.URL, &w.BrokenAt, &w.BrokenReason, &w.UpdatedAt)
	return &w, err
}

// GetWebhooks returns the user's webhooks keyed by kind.
func (s *Store) GetWebhooks(ctx context.Context, userID int64) (map[string]*Webhook, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, url, broken_at, broken_reason, updated_at FROM webhooks WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Webhook{}
	for rows.Next() {
		var w Webhook
		if err := rows.Scan(&w.Kind, &w.URL, &w.BrokenAt, &w.BrokenReason, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out[w.Kind] = &w
	}
	return out, rows.Err()
}

// GetWebhook returns one webhook or ErrNotFound.
func (s *Store) GetWebhook(ctx context.Context, userID int64, kind string) (*Webhook, error) {
	ws, err := s.GetWebhooks(ctx, userID)
	if err != nil {
		return nil, err
	}
	if w, ok := ws[kind]; ok {
		return w, nil
	}
	return nil, ErrNotFound
}

// DeleteWebhook removes a webhook; ErrNotFound if absent.
func (s *Store) DeleteWebhook(ctx context.Context, userID int64, kind string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM webhooks WHERE user_id = $1 AND kind = $2`, userID, kind)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// MarkWebhookBroken flags a webhook; reason must not contain the URL.
func (s *Store) MarkWebhookBroken(ctx context.Context, userID int64, kind, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE webhooks SET broken_at = now(), broken_reason = $3 WHERE user_id = $1 AND kind = $2`, userID, kind, reason)
	return err
}

// ClearWebhookBroken removes the broken flag.
func (s *Store) ClearWebhookBroken(ctx context.Context, userID int64, kind string) error {
	_, err := s.pool.Exec(ctx, `UPDATE webhooks SET broken_at = NULL, broken_reason = NULL WHERE user_id = $1 AND kind = $2`, userID, kind)
	return err
}

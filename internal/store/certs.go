package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Cert is one certificate row. KeyEnc is the private key encrypted under the
// owner's DEK.
type Cert struct {
	ID        string
	UserID    int64
	Name      string
	Hosts     []string
	Bits      int
	Days      int
	Subject   json.RawMessage
	CNF       string
	CertPEM   string
	KeyEnc    []byte
	NotBefore time.Time
	NotAfter  time.Time
	CreatedAt time.Time
	RenewedBy *string
}

const certCols = `id::text, user_id, name, hosts, bits, days, subject, cnf, cert_pem, key_enc, not_before, not_after, created_at, renewed_by::text`

func scanCert(row pgx.Row) (*Cert, error) {
	var c Cert
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Hosts, &c.Bits, &c.Days, &c.Subject, &c.CNF, &c.CertPEM, &c.KeyEnc,
		&c.NotBefore, &c.NotAfter, &c.CreatedAt, &c.RenewedBy)
	if err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// InsertCert stores c (ID, UserID and the key already set). If renewOf is not
// empty it also marks that cert as renewed by c in the same transaction:
// ErrNotFound if it is unknown or not the user's, ErrAlreadyRenewed if it was.
func (s *Store) InsertCert(ctx context.Context, c *Cert, renewOf string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if renewOf != "" {
		var renewed *string
		err := tx.QueryRow(ctx, `SELECT renewed_by::text FROM certs WHERE id = $1 AND user_id = $2 FOR UPDATE`, renewOf, c.UserID).Scan(&renewed)
		if err != nil {
			return notFound(err)
		}
		if renewed != nil {
			return ErrAlreadyRenewed
		}
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO certs (id, user_id, name, hosts, bits, days, subject, cnf, cert_pem, key_enc, not_before, not_after)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING created_at`,
		c.ID, c.UserID, c.Name, c.Hosts, c.Bits, c.Days, []byte(c.Subject), c.CNF, c.CertPEM, c.KeyEnc, c.NotBefore, c.NotAfter,
	).Scan(&c.CreatedAt)
	if err != nil {
		return err
	}
	if renewOf != "" {
		if _, err := tx.Exec(ctx, `UPDATE certs SET renewed_by = $1 WHERE id = $2 AND user_id = $3`, c.ID, renewOf, c.UserID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetCert returns the user's cert or ErrNotFound (also for another user's id).
func (s *Store) GetCert(ctx context.Context, userID int64, id string) (*Cert, error) {
	return scanCert(s.pool.QueryRow(ctx, `SELECT `+certCols+` FROM certs WHERE id = $1 AND user_id = $2`, id, userID))
}

// ListCerts returns the user's certs, newest first.
func (s *Store) ListCerts(ctx context.Context, userID int64) ([]*Cert, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+certCols+` FROM certs WHERE user_id = $1 ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Cert{}
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCert removes the user's cert; ErrNotFound if absent. A cert that was
// renewed by it has its renewed_by cleared by the foreign key.
func (s *Store) DeleteCert(ctx context.Context, userID int64, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM certs WHERE id = $1 AND user_id = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Candidate is a cert that may need a reminder, with its owner's webhook.
type Candidate struct {
	CertID   string
	UserID   int64
	Name     string
	Hosts    []string
	NotAfter time.Time
	Kind     string
	URL      string
}

// ReminderCandidates returns un-renewed certs expiring before now+8 days (or
// already expired) whose owner has a non-broken webhook, ordered per webhook.
func (s *Store) ReminderCandidates(ctx context.Context, now time.Time) ([]Candidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.id::text, c.user_id, c.name, c.hosts, c.not_after, w.kind, w.url
		 FROM certs c JOIN webhooks w ON w.user_id = c.user_id
		 WHERE c.renewed_by IS NULL AND w.broken_at IS NULL AND c.not_after < $1::timestamptz + interval '8 days'
		 ORDER BY c.user_id, w.kind, c.not_after`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.CertID, &c.UserID, &c.Name, &c.Hosts, &c.NotAfter, &c.Kind, &c.URL); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ClaimReminder records (cert, kind, mark); it reports whether this call
// claimed it (false = already sent or claimed).
func (s *Store) ClaimReminder(ctx context.Context, certID, kind, mark string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO reminders_sent (cert_id, kind, mark) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, certID, kind, mark)
	return tag.RowsAffected() == 1, err
}

// UnclaimReminder deletes a claim so the next tick retries it.
func (s *Store) UnclaimReminder(ctx context.Context, certID, kind, mark string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM reminders_sent WHERE cert_id = $1 AND kind = $2 AND mark = $3`, certID, kind, mark)
	return err
}

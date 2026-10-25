package platform

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Glhby13/drawio-custom-service/internal/model"
)

// AIPolicy is the per-document switch and limits for AI collaborators (ADR-0009).
type AIPolicy struct {
	Allowed      bool `json:"allowed"`
	MaxOpsPerMin int  `json:"maxOpsPerMin"`
	MaxDelete    int  `json:"maxDelete"`
}

// DocMeta is a row of documents.
type DocMeta struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Owner       string    `json:"owner"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	HeadSeq     int64     `json:"headSeq"`
	SnapshotSeq int64     `json:"-"`
	AI          AIPolicy  `json:"aiPolicy"`
}

// Perm is one share entry.
type Perm struct {
	Subject   string    `json:"subject"`
	Role      string    `json:"role"`
	GrantedBy string    `json:"grantedBy"`
	GrantedAt time.Time `json:"grantedAt"`
}

// Store is the PostgreSQL access layer.
type Store struct{ pool *pgxpool.Pool }

var errNotFound = errors.New("not found")

func newID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

const docCols = `id, title, owner, created_at, updated_at, head_seq, snapshot_seq, ai_policy`

func scanDoc(row pgx.Row) (*DocMeta, error) {
	var d DocMeta
	var ai []byte
	if err := row.Scan(&d.ID, &d.Title, &d.Owner, &d.CreatedAt, &d.UpdatedAt, &d.HeadSeq, &d.SnapshotSeq, &ai); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(ai, &d.AI)
	return &d, nil
}

// GetDocument returns a non-deleted document.
func (s *Store) GetDocument(ctx context.Context, id string) (*DocMeta, error) {
	return scanDoc(s.pool.QueryRow(ctx, `SELECT `+docCols+` FROM documents WHERE id=$1 AND deleted_at IS NULL`, id))
}

// CreateDocument inserts the row (the snapshot is written by the caller before, so a row never exists without state).
func (s *Store) CreateDocument(ctx context.Context, id, title, owner string, ai AIPolicy) error {
	b, _ := json.Marshal(ai)
	_, err := s.pool.Exec(ctx, `INSERT INTO documents (id, title, owner, ai_policy) VALUES ($1,$2,$3,$4)`, id, title, owner, b)
	return err
}

// DocListItem is one row of the document list.
type DocListItem struct {
	DocMeta
	Role string `json:"role"`
}

// ListDocuments returns documents the subjects can see (owner or shared); admins see all.
func (s *Store) ListDocuments(ctx context.Context, username string, admin bool) ([]DocListItem, error) {
	q := `SELECT d.id, d.title, d.owner, d.created_at, d.updated_at, d.head_seq, d.snapshot_seq, d.ai_policy,
	        CASE WHEN d.owner=$1 THEN 'owner' ELSE COALESCE((SELECT p.role FROM permissions p WHERE p.document_id=d.id AND p.subject=$1),
	             (SELECT p.role FROM permissions p WHERE p.document_id=d.id AND p.subject='*'), '') END AS role
	      FROM documents d WHERE d.deleted_at IS NULL`
	if !admin {
		q += ` AND (d.owner=$1 OR EXISTS (SELECT 1 FROM permissions p WHERE p.document_id=d.id AND p.subject IN ($1,'*')))`
	}
	q += ` ORDER BY d.updated_at DESC LIMIT 500`
	rows, err := s.pool.Query(ctx, q, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DocListItem{}
	for rows.Next() {
		var it DocListItem
		var ai []byte
		if err := rows.Scan(&it.ID, &it.Title, &it.Owner, &it.CreatedAt, &it.UpdatedAt, &it.HeadSeq, &it.SnapshotSeq, &ai, &it.Role); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(ai, &it.AI)
		out = append(out, it)
	}
	return out, rows.Err()
}

// RoleOf returns the stored role of subject on the document ("" none).
func (s *Store) RoleOf(ctx context.Context, docID, subject string) (string, error) {
	var role string
	err := s.pool.QueryRow(ctx, `SELECT role FROM permissions WHERE document_id=$1 AND subject=$2`, docID, subject).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// Perms lists the shares of a document.
func (s *Store) Perms(ctx context.Context, docID string) ([]Perm, error) {
	rows, err := s.pool.Query(ctx, `SELECT subject, role, granted_by, granted_at FROM permissions WHERE document_id=$1 ORDER BY subject`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Perm{}
	for rows.Next() {
		var p Perm
		if err := rows.Scan(&p.Subject, &p.Role, &p.GrantedBy, &p.GrantedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPerm creates/updates (role != "") or removes (role == "") a share.
func (s *Store) SetPerm(ctx context.Context, docID, subject, role, by string) error {
	if role == "" {
		_, err := s.pool.Exec(ctx, `DELETE FROM permissions WHERE document_id=$1 AND subject=$2`, docID, subject)
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO permissions (document_id, subject, role, granted_by) VALUES ($1,$2,$3,$4)
	  ON CONFLICT (document_id, subject) DO UPDATE SET role=EXCLUDED.role, granted_by=EXCLUDED.granted_by, granted_at=now()`, docID, subject, role, by)
	return err
}

// SetTitle renames.
func (s *Store) SetTitle(ctx context.Context, id, title string) error {
	_, err := s.pool.Exec(ctx, `UPDATE documents SET title=$2, updated_at=now() WHERE id=$1 AND deleted_at IS NULL`, id, title)
	return err
}

// SoftDelete hides a document (state is kept for audit/DR).
func (s *Store) SoftDelete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE documents SET deleted_at=now() WHERE id=$1 AND deleted_at IS NULL`, id)
	return err
}

// SetAIPolicy stores the policy.
func (s *Store) SetAIPolicy(ctx context.Context, id string, p AIPolicy) error {
	b, _ := json.Marshal(p)
	_, err := s.pool.Exec(ctx, `UPDATE documents SET ai_policy=$2 WHERE id=$1`, id, b)
	return err
}

// UpsertUser records a principal (used by the share picker and audit).
func (s *Store) UpsertUser(ctx context.Context, sub, username, name, email, kind string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO users (sub, username, name, email, kind) VALUES ($1,$2,$3,$4,$5)
	  ON CONFLICT (sub) DO UPDATE SET username=EXCLUDED.username, name=EXCLUDED.name, email=EXCLUDED.email, kind=EXCLUDED.kind, last_seen=now()`, sub, username, name, email, kind)
	return err
}

// UserHit is a share-picker suggestion.
type UserHit struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// SearchUsers returns users seen by the platform whose username/name contains q.
func (s *Store) SearchUsers(ctx context.Context, q string) ([]UserHit, error) {
	rows, err := s.pool.Query(ctx, `SELECT username, COALESCE(name,''), kind FROM users WHERE username ILIKE $1 OR name ILIKE $1 ORDER BY username LIMIT 20`, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserHit{}
	for rows.Next() {
		var u UserHit
		if err := rows.Scan(&u.Username, &u.Name, &u.Kind); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// LoadOps returns resolved ops with after < seq <= upto in order.
func (s *Store) LoadOps(ctx context.Context, docID string, after, upto int64) ([]model.Op, error) {
	rows, err := s.pool.Query(ctx, `SELECT op FROM ops WHERE document_id=$1 AND seq>$2 AND seq<=$3 ORDER BY seq`, docID, after, upto)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Op
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var o model.Op
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// LoadBatch returns the ops of one batch.
func (s *Store) LoadBatch(ctx context.Context, docID, batch string) ([]model.Op, error) {
	rows, err := s.pool.Query(ctx, `SELECT op FROM ops WHERE document_id=$1 AND batch=$2 ORDER BY seq`, docID, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Op
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var o model.Op
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// HistoryItem groups the ops of one batch for the history panel.
type HistoryItem struct {
	Batch    string    `json:"batch"`
	FromSeq  int64     `json:"fromSeq"`
	ToSeq    int64     `json:"toSeq"`
	At       time.Time `json:"at"`
	ActorID  string    `json:"actor"`
	Type     string    `json:"actorType"`
	OnBehalf string    `json:"onBehalfOf,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Model    string    `json:"model,omitempty"`
	Count    int       `json:"ops"`
	Types    []string  `json:"types"`
}

// History returns the latest batches, newest first.
func (s *Store) History(ctx context.Context, docID string, limit int, beforeSeq int64) ([]HistoryItem, error) {
	if beforeSeq <= 0 {
		beforeSeq = 1 << 60
	}
	rows, err := s.pool.Query(ctx, `SELECT batch, min(seq), max(seq), max(created_at), actor_id, actor_type, COALESCE(obo,''), COALESCE(agent,''), COALESCE(model,''), count(*), array_agg(DISTINCT type)
	  FROM ops WHERE document_id=$1 AND seq<$2 GROUP BY batch, actor_id, actor_type, obo, agent, model ORDER BY max(seq) DESC LIMIT $3`, docID, beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryItem{}
	for rows.Next() {
		var h HistoryItem
		if err := rows.Scan(&h.Batch, &h.FromSeq, &h.ToSeq, &h.At, &h.ActorID, &h.Type, &h.OnBehalf, &h.Agent, &h.Model, &h.Count, &h.Types); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// LatestSnapshot returns the newest snapshot at or below seq (0 = any).
func (s *Store) LatestSnapshot(ctx context.Context, docID string) (seq int64, key string, err error) {
	err = s.pool.QueryRow(ctx, `SELECT seq, s3_key FROM snapshots WHERE document_id=$1 ORDER BY seq DESC LIMIT 1`, docID).Scan(&seq, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", errNotFound
	}
	return
}

// AddSnapshot records a stored snapshot and advances documents.snapshot_seq.
func (s *Store) AddSnapshot(ctx context.Context, docID string, seq int64, key, sum string, size int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO snapshots (document_id, seq, s3_key, sha256, size) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, docID, seq, key, sum, size); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE documents SET snapshot_seq=GREATEST(snapshot_seq,$2) WHERE id=$1`, docID, seq)
		return err
	})
}

// Version is a named point in the history.
type Version struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Seq       int64     `json:"seq"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// AddVersion records a named version.
func (s *Store) AddVersion(ctx context.Context, v Version, docID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO versions (id, document_id, name, seq, created_by) VALUES ($1,$2,$3,$4,$5)`, v.ID, docID, v.Name, v.Seq, v.CreatedBy)
	return err
}

// Versions lists them, newest first.
func (s *Store) Versions(ctx context.Context, docID string) ([]Version, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, seq, created_by, created_at FROM versions WHERE document_id=$1 ORDER BY created_at DESC LIMIT 200`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.Name, &v.Seq, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVersion returns one version of the document.
func (s *Store) GetVersion(ctx context.Context, docID, id string) (*Version, error) {
	var v Version
	err := s.pool.QueryRow(ctx, `SELECT id, name, seq, created_by, created_at FROM versions WHERE document_id=$1 AND id=$2`, docID, id).Scan(&v.ID, &v.Name, &v.Seq, &v.CreatedBy, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	return &v, err
}

// Comment is a note on a cell/page.
type Comment struct {
	ID         string    `json:"id"`
	Page       string    `json:"page"`
	Cell       string    `json:"cell"`
	Author     string    `json:"author"`
	AuthorType string    `json:"authorType"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
	Resolved   bool      `json:"resolved"`
}

// AddComment stores a comment.
func (s *Store) AddComment(ctx context.Context, docID string, c Comment) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO comments (id, document_id, page, cell, author, author_type, body) VALUES ($1,$2,$3,$4,$5,$6,$7)`, c.ID, docID, c.Page, c.Cell, c.Author, c.AuthorType, c.Body)
	return err
}

// Comments lists a document's comments.
func (s *Store) Comments(ctx context.Context, docID string) ([]Comment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, page, cell, author, author_type, body, created_at, resolved FROM comments WHERE document_id=$1 ORDER BY created_at LIMIT 500`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.Page, &c.Cell, &c.Author, &c.AuthorType, &c.Body, &c.CreatedAt, &c.Resolved); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Audit appends an audit event; failures are logged by the caller, never fatal.
func (s *Store) Audit(ctx context.Context, a model.Actor, docID, action, outcome string, detail any) error {
	b, _ := json.Marshal(detail)
	var obo, doc *string
	if a.OnBehalfOf != "" {
		obo = &a.OnBehalfOf
	}
	if docID != "" {
		doc = &docID
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events (actor_id, actor_type, obo, document_id, action, outcome, detail) VALUES ($1,$2,$3,$4,$5,$6,$7)`, a.ID, a.Type, obo, doc, action, outcome, b)
	return err
}

// AuditEvent is a row for the audit API.
type AuditEvent struct {
	ID       int64           `json:"id"`
	At       time.Time       `json:"at"`
	Actor    string          `json:"actor"`
	Type     string          `json:"actorType"`
	OnBehalf string          `json:"onBehalfOf,omitempty"`
	Action   string          `json:"action"`
	Outcome  string          `json:"outcome"`
	Detail   json.RawMessage `json:"detail"`
}

// AuditFor returns the latest audit rows of a document.
func (s *Store) AuditFor(ctx context.Context, docID string, limit int) ([]AuditEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, at, actor_id, actor_type, COALESCE(obo,''), action, outcome, detail FROM audit_events WHERE document_id=$1 ORDER BY id DESC LIMIT $2`, docID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Type, &e.OnBehalf, &e.Action, &e.Outcome, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NewStore wraps a pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

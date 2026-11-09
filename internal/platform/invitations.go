package platform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Glhby13/drawio-custom-service/internal/model"
)

// Invitation-based sharing (ADR-0021). Giving somebody access never takes effect by itself: the owner creates an
// invitation, the recipient opens its link, and ONLY accepting it writes the `permissions` row. Three kinds share one table:
//
//	user + e-mail   kind 'user', bound to one account, the gateway mails the link
//	user, no e-mail kind 'user', bound to one account, the owner copies the link
//	link            kind 'link', anybody who is signed in, many uses until it expires or is revoked
//
// Only the SHA-256 of a token is stored. The token is returned once, to the creating owner.

// InvitePrefix marks a DrawSpace invitation token.
const InvitePrefix = "dsi_"

const (
	defaultInviteDays     = 7
	maxInviteDays         = 365
	maxLinksPerDoc        = 20
	maxPendingUserInvites = 50
	// safety valves ON TOP of "one e-mail per sender, recipient and diagram per day": a sender could otherwise open many
	// diagrams and mail the same person once from each (reviewer finding, 2026-11-09)
	maxEmailsPerPairPerDay   = 10  // one sender -> one recipient, all diagrams together
	maxEmailsPerSenderPerDay = 100 // one sender, all recipients
)

// wib is Indonesian Western Time (no daylight saving): the daily e-mail limit resets at 00:00 here.
var wib = time.FixedZone("WIB", 7*3600)

var (
	errInviteInvalid  = errors.New("invitation invalid") // unknown, expired, revoked or already used: one answer for all (no oracle)
	errInviteNotYours = errors.New("invitation bound to another account")
	errInviteOwner    = errors.New("owner of the diagram")
)

// Invitation is a row of invitations (never the token).
type Invitation struct {
	ID          string     `json:"id"`
	DocumentID  string     `json:"documentId,omitempty"`
	Kind        string     `json:"kind"`
	Subject     string     `json:"subject,omitempty"`
	Role        string     `json:"role"`
	Hint        string     `json:"hint"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Status      string     `json:"status"`
	State       string     `json:"state"` // pending | expired | accepted | declined | revoked
	EmailSentAt *time.Time `json:"emailSentAt"`
	UseCount    int        `json:"useCount"`
	// Recoverable: the link can be shown again (it was minted with INVITE_TOKEN_KEY, which is still configured)
	Recoverable bool `json:"recoverable"`
}

func (i *Invitation) fillState(now time.Time) {
	i.State = i.Status
	if i.Status == "pending" && i.ExpiresAt != nil && !i.ExpiresAt.After(now) {
		i.State = "expired"
	}
}

func (i *Invitation) live(now time.Time) bool { i.fillState(now); return i.State == "pending" }

// deriveInviteToken computes the token of an invitation from the server key, the invitation id and its salt (ADR-0021 amendment).
func deriveInviteToken(key []byte, id string, salt []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("dsi1|" + id + "|"))
	m.Write(salt)
	return InvitePrefix + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// mintInviteToken makes the token of invitation `id`: derived (re-computable, with a salt to store) when INVITE_TOKEN_KEY is set,
// random and shown once otherwise.
func (s *Server) mintInviteToken(id string) (token, hash string, salt []byte, err error) {
	if k := s.cfg.InviteTokenKey; len(k) >= 32 {
		salt = make([]byte, 16)
		if _, err = rand.Read(salt); err != nil {
			return "", "", nil, err
		}
		token = deriveInviteToken(k, id, salt)
		return token, hashToken(token), salt, nil
	}
	token, hash, err = newInviteToken()
	return token, hash, nil, err
}

func newInviteToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = InvitePrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

// inviteExpiry turns expiresInDays (absent = 7, 0 = never, 1..365) into an expiry time.
func inviteExpiry(days *int, now time.Time) (*time.Time, bool) {
	d := defaultInviteDays
	if days != nil {
		d = *days
	}
	if d < 0 || d > maxInviteDays {
		return nil, false
	}
	if d == 0 {
		return nil, true
	}
	t := now.Add(time.Duration(d) * 24 * time.Hour).UTC()
	return &t, true
}

// startOfDayWIB is 00:00 WIB of the day that contains t; nextMidnightWIB is the following 00:00 WIB.
func startOfDayWIB(t time.Time) time.Time {
	l := t.In(wib)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, wib)
}

func nextMidnightWIB(t time.Time) time.Time { return startOfDayWIB(t).AddDate(0, 0, 1) }

// ---- store -----------------------------------------------------------------------------------------------------------

const invCols = `id, document_id, kind, COALESCE(subject,''), role, hint, created_by, created_at, expires_at, status, email_sent_at, use_count, (token_salt IS NOT NULL)`

func scanInvitation(row pgx.Row) (*Invitation, error) {
	var i Invitation
	if err := row.Scan(&i.ID, &i.DocumentID, &i.Kind, &i.Subject, &i.Role, &i.Hint, &i.CreatedBy, &i.CreatedAt, &i.ExpiresAt, &i.Status, &i.EmailSentAt, &i.UseCount, &i.Recoverable); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errNotFound
		}
		return nil, err
	}
	i.fillState(time.Now())
	return &i, nil
}

var (
	errAlreadyHasAccess = errors.New("already has this access")
	errTooManyInvites   = errors.New("too many invitations")
	errEmailCooldown    = errors.New("invitation e-mail already sent today")
	errEmailQuota       = errors.New("too many invitation e-mails today")
)

// errAlreadyPending carries the id of the invitation that is already waiting for the same person.
type errAlreadyPending struct{ id string }

func (e errAlreadyPending) Error() string { return "an invitation for this person is already waiting" }

type pgQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// lockKey serialises everything that decides on one (diagram, person) pair for the rest of the transaction, so a burst of
// parallel requests cannot all pass the checks before the first row exists (no e-mail flood, no duplicate invitations).
func lockKey(ctx context.Context, q pgQuerier, key string) error {
	var x int
	return q.QueryRow(ctx, `SELECT 1 FROM (SELECT pg_advisory_xact_lock(hashtextextended($1, 0))) l`, key).Scan(&x)
}

func emailedSince(ctx context.Context, q pgQuerier, docID, subject, by string, since time.Time) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE document_id=$1 AND kind='user' AND subject=$2 AND created_by=$3 AND email_sent_at >= $4`, docID, subject, by, since).Scan(&n)
	return n > 0, err
}

// emailQuota enforces the cross-diagram daily valves (call it under the sender's lock).
func emailQuota(ctx context.Context, q pgQuerier, by, subject string, since time.Time) error {
	var pair, total int
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE subject=$2), count(*) FROM invitations WHERE kind='user' AND created_by=$1 AND email_sent_at >= $3`, by, subject, since).Scan(&pair, &total); err != nil {
		return err
	}
	if pair >= maxEmailsPerPairPerDay || total >= maxEmailsPerSenderPerDay {
		return errEmailQuota
	}
	return nil
}

// CreateInvitation checks the rules and stores the invitation in one transaction under a per-person lock. With reserveEmail the
// day's one e-mail (per sender, recipient and diagram; hidden and revoked rows count too, so cancel-and-recreate buys nothing)
// is taken NOW by setting email_sent_at; the gateway gives it back through ReleaseEmail when the mail cannot be sent.
func (s *Store) CreateInvitation(ctx context.Context, i Invitation, hash string, salt []byte, reserveEmail bool, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	limit, key := maxLinksPerDoc, i.DocumentID+"|link"
	if i.Kind == "user" {
		limit, key = maxPendingUserInvites, i.DocumentID+"|user" // one lock for the whole diagram: the caps count across all people
	}
	if reserveEmail { // lock order everywhere: sender first, then diagram
		if err := lockKey(ctx, tx, "mail|"+i.CreatedBy); err != nil {
			return err
		}
	}
	if err := lockKey(ctx, tx, key); err != nil {
		return err
	}
	if i.Kind == "user" {
		var cur string
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT role FROM permissions WHERE document_id=$1 AND subject=$2),'')`, i.DocumentID, i.Subject).Scan(&cur); err != nil {
			return err
		}
		if rank(cur) >= rank(i.Role) {
			return errAlreadyHasAccess
		}
		var pid string
		err := tx.QueryRow(ctx, `SELECT id FROM invitations WHERE document_id=$1 AND kind='user' AND subject=$2 AND status='pending' AND hidden_at IS NULL AND (expires_at IS NULL OR expires_at > now()) LIMIT 1`, i.DocumentID, i.Subject).Scan(&pid)
		if err == nil {
			return errAlreadyPending{pid}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if reserveEmail {
			if sent, err := emailedSince(ctx, tx, i.DocumentID, i.Subject, i.CreatedBy, startOfDayWIB(now)); err != nil {
				return err
			} else if sent {
				return errEmailCooldown
			}
			if err := emailQuota(ctx, tx, i.CreatedBy, i.Subject, startOfDayWIB(now)); err != nil {
				return err
			}
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE document_id=$1 AND kind=$2 AND status='pending' AND hidden_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, i.DocumentID, i.Kind).Scan(&n); err != nil {
		return err
	}
	if n >= limit {
		return errTooManyInvites
	}
	var subj *string
	if i.Subject != "" {
		subj = &i.Subject
	}
	var sent *time.Time
	if reserveEmail {
		sent = &now
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invitations (id, document_id, kind, subject, role, token_hash, hint, created_by, expires_at, email_sent_at, token_salt) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		i.ID, i.DocumentID, i.Kind, subj, i.Role, hash, i.Hint, i.CreatedBy, i.ExpiresAt, sent, salt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResendInvitation gives a waiting 'user' invitation a fresh token and expiry and takes the day's e-mail, under the same locks.
// An expired invitation cannot be resent (create a new one), and neither can one whose person has access in the meantime.
func (s *Store) ResendInvitation(ctx context.Context, docID, id, by, hash, hint string, salt []byte, exp *time.Time, now time.Time) (*Invitation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockKey(ctx, tx, "mail|"+by); err != nil {
		return nil, err
	}
	if err := lockKey(ctx, tx, docID+"|user"); err != nil {
		return nil, err
	}
	var subject, role string
	if err := tx.QueryRow(ctx, `SELECT subject, role FROM invitations WHERE document_id=$1 AND id=$2 AND kind='user' AND status='pending' AND hidden_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, docID, id).Scan(&subject, &role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errNotFound
		}
		return nil, err
	}
	var cur string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT role FROM permissions WHERE document_id=$1 AND subject=$2),'')`, docID, subject).Scan(&cur); err != nil {
		return nil, err
	}
	if rank(cur) >= rank(role) {
		return nil, errAlreadyHasAccess
	}
	if sent, err := emailedSince(ctx, tx, docID, subject, by, startOfDayWIB(now)); err != nil {
		return nil, err
	} else if sent {
		return nil, errEmailCooldown
	}
	if err := emailQuota(ctx, tx, by, subject, startOfDayWIB(now)); err != nil {
		return nil, err
	}
	inv, err := scanInvitation(tx.QueryRow(ctx, `UPDATE invitations SET token_hash=$3, hint=$4, expires_at=$5, email_sent_at=$6, token_salt=$7 WHERE document_id=$1 AND id=$2 AND status='pending' RETURNING `+invCols, docID, id, hash, hint, exp, now, salt))
	if err != nil {
		return nil, err
	}
	return inv, tx.Commit(ctx)
}

// ReleaseEmail gives the day's e-mail back after the gateway could not send it (only for a reservation made moments ago).
func (s *Store) ReleaseEmail(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE invitations SET email_sent_at=NULL WHERE id=$1 AND kind='user' AND status='pending' AND email_sent_at > now() - interval '10 minutes'`, id)
	return tag.RowsAffected() == 1, err
}

// Invitations lists what the owner still sees (hidden rows stay in the table for the daily limit and the audit trail).
func (s *Store) Invitations(ctx context.Context, docID string) ([]Invitation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+invCols+` FROM invitations WHERE document_id=$1 AND hidden_at IS NULL ORDER BY created_at DESC LIMIT 200`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		i, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// GetInvitation loads one invitation of a document.
func (s *Store) GetInvitation(ctx context.Context, docID, id string) (*Invitation, error) {
	return scanInvitation(s.pool.QueryRow(ctx, `SELECT `+invCols+` FROM invitations WHERE document_id=$1 AND id=$2 AND hidden_at IS NULL`, docID, id))
}

// RevokeInvitation turns a live invitation into revoked.
func (s *Store) RevokeInvitation(ctx context.Context, docID, id, by string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE invitations SET status='revoked', decided_by=$3, decided_at=now() WHERE document_id=$1 AND id=$2 AND status='pending' AND (expires_at IS NULL OR expires_at > now())`, docID, id, by)
	return tag.RowsAffected() == 1, err
}

// HideInvitation removes a dead invitation from the owner's list (the row stays).
func (s *Store) HideInvitation(ctx context.Context, docID, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE invitations SET hidden_at=now() WHERE document_id=$1 AND id=$2 AND hidden_at IS NULL AND (status<>'pending' OR (expires_at IS NOT NULL AND expires_at <= now()))`, docID, id)
	return tag.RowsAffected() == 1, err
}

// InvitePreview is what a recipient sees before deciding.
type InvitePreview struct {
	Invitation
	DocTitle   string
	DocOwner   string
	InviterOf  string // display name of the inviter
	AlreadyHas string // the role the recipient already has on the diagram ("" none)
}

// InviteTokenParts returns what is needed to compute an invitation's token again, and whether the invitation is still live.
func (s *Store) InviteTokenParts(ctx context.Context, docID, id string) (hash string, salt []byte, live bool, err error) {
	var status string
	var exp *time.Time
	err = s.pool.QueryRow(ctx, `SELECT token_hash, token_salt, status, expires_at FROM invitations WHERE document_id=$1 AND id=$2 AND hidden_at IS NULL`, docID, id).Scan(&hash, &salt, &status, &exp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, false, errNotFound
	}
	return hash, salt, status == "pending" && (exp == nil || exp.After(time.Now())), err
}

// previewByHash resolves a token for a recipient: the invitation must be live, the diagram must exist, and a 'user'
// invitation must be addressed to this very account. Everything else is the one errInviteInvalid answer.
func (s *Store) previewByHash(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, hash, username string, lock bool) (*InvitePreview, error) {
	sqlText := `SELECT i.id, i.document_id, i.kind, COALESCE(i.subject,''), i.role, i.hint, i.created_by, i.created_at, i.expires_at, i.status, i.email_sent_at, i.use_count,
	       d.title, d.owner, COALESCE(NULLIF(u.name,''), i.created_by),
	       COALESCE((SELECT p.role FROM permissions p WHERE p.document_id=i.document_id AND p.subject=$2),'')
	  FROM invitations i JOIN documents d ON d.id=i.document_id AND d.deleted_at IS NULL
	  LEFT JOIN users u ON u.username=i.created_by
	 WHERE i.token_hash=$1`
	if lock {
		sqlText += ` FOR UPDATE OF i`
	}
	var pv InvitePreview
	err := q.QueryRow(ctx, sqlText, hash, username).Scan(&pv.ID, &pv.DocumentID, &pv.Kind, &pv.Subject, &pv.Role, &pv.Hint, &pv.CreatedBy, &pv.CreatedAt, &pv.ExpiresAt, &pv.Status, &pv.EmailSentAt, &pv.UseCount,
		&pv.DocTitle, &pv.DocOwner, &pv.InviterOf, &pv.AlreadyHas)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errInviteInvalid
	}
	if err != nil {
		return nil, err
	}
	if !pv.live(time.Now()) {
		return nil, errInviteInvalid
	}
	if pv.Kind == "user" && !strings.EqualFold(pv.Subject, username) { // Keycloak and local accounts are lower case; be tolerant, never wider
		return nil, errInviteNotYours
	}
	if pv.DocOwner == username {
		return nil, errInviteOwner
	}
	return &pv, nil
}

// PreviewInvitation answers GET /invitations/{token}.
func (s *Store) PreviewInvitation(ctx context.Context, token, username string) (*InvitePreview, error) {
	return s.previewByHash(ctx, s.pool, hashToken(token), username, false)
}

// AcceptInvitation writes the share (never lowering an existing, higher role) and consumes a 'user' invitation, atomically.
func (s *Store) AcceptInvitation(ctx context.Context, token, username string) (*InvitePreview, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	pv, err := s.previewByHash(ctx, tx, hashToken(token), username, true)
	if err != nil {
		return nil, "", err
	}
	role := pv.Role
	if rank(pv.AlreadyHas) >= rank(role) {
		role = pv.AlreadyHas
	} else if _, err := tx.Exec(ctx, `INSERT INTO permissions (document_id, subject, role, granted_by) VALUES ($1,$2,$3,$4)
	  ON CONFLICT (document_id, subject) DO UPDATE SET role=EXCLUDED.role, granted_by=EXCLUDED.granted_by, granted_at=now()`, pv.DocumentID, username, role, pv.CreatedBy); err != nil {
		return nil, "", err
	}
	if pv.Kind == "user" {
		_, err = tx.Exec(ctx, `UPDATE invitations SET status='accepted', decided_by=$2, decided_at=now(), use_count=use_count+1 WHERE id=$1`, pv.ID, username)
	} else {
		_, err = tx.Exec(ctx, `UPDATE invitations SET use_count=use_count+1 WHERE id=$1`, pv.ID)
	}
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return pv, role, nil
}

// DeclineInvitation marks a 'user' invitation declined. A link is not consumed by one person saying no.
func (s *Store) DeclineInvitation(ctx context.Context, token, username string) (*InvitePreview, error) {
	pv, err := s.previewByHash(ctx, s.pool, hashToken(token), username, false)
	if err != nil {
		return nil, err
	}
	if pv.Kind == "user" {
		if _, err := s.pool.Exec(ctx, `UPDATE invitations SET status='declined', decided_by=$2, decided_at=now() WHERE id=$1 AND status='pending'`, pv.ID, username); err != nil {
			return nil, err
		}
	}
	return pv, nil
}

// Contact is what the gateway needs to mail somebody: the e-mail the platform knows (local accounts, signed-in users).
type Contact struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	Found bool   `json:"found"`
}

// ContactOf looks a username up in local accounts first (the authoritative e-mail), then in the users seen so far.
func (s *Store) ContactOf(ctx context.Context, username string) (Contact, error) {
	var c Contact
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(email,''), COALESCE(display_name,'') FROM local_users WHERE lower(username)=lower($1)`, username).Scan(&c.Email, &c.Name)
	if err == nil {
		c.Found = true
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	err = s.pool.QueryRow(ctx, `SELECT COALESCE(email,''), COALESCE(name,'') FROM users WHERE lower(username)=lower($1)`, username).Scan(&c.Email, &c.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	c.Found = err == nil
	return c, err
}

// ---- handlers: the owner's side ---------------------------------------------------------------------------------------------

type inviteReq struct {
	Kind          string `json:"kind"`    // "user" | "link"
	Subject       string `json:"subject"` // username, kind "user" only
	Role          string `json:"role"`    // viewer | editor
	ExpiresInDays *int   `json:"expiresInDays"`
	SendEmail     bool   `json:"sendEmail"` // the gateway will mail this one: the daily limit applies
}

// ownerSide is the common gate for creating/listing/changing invitations of a diagram: sharing rights, humans only.
func (s *Server) ownerSide(w http.ResponseWriter, r *http.Request, p *Principal) (*DocMeta, bool) {
	d, _, ok := s.access(w, r, p, ActShare)
	if !ok {
		return nil, false
	}
	if p.IsAI || p.TokenDoc != "" {
		writeErr(w, http.StatusForbidden, "forbidden", "not available to AI actors")
		return nil, false
	}
	return d, true
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, ok := s.ownerSide(w, r, p)
	if !ok {
		return
	}
	var in inviteReq
	if !readJSON(w, r, &in, 4096) {
		return
	}
	in.Subject = strings.ToLower(strings.TrimSpace(in.Subject))
	if in.Role != RoleViewer && in.Role != RoleEditor {
		writeErr(w, http.StatusBadRequest, "bad_request", "role must be viewer or editor")
		return
	}
	if in.Kind != "user" && in.Kind != "link" {
		writeErr(w, http.StatusBadRequest, "bad_request", "kind must be user or link")
		return
	}
	now := time.Now()
	exp, valid := inviteExpiry(in.ExpiresInDays, now)
	if !valid {
		writeErr(w, http.StatusBadRequest, "bad_request", "expiresInDays 0..365 (0 = never)")
		return
	}
	ctx := r.Context()
	if in.Kind == "user" {
		if !subjectRe.MatchString(in.Subject) || in.Subject == d.Owner {
			writeErr(w, http.StatusBadRequest, "bad_request", "subject must be a username, and not the owner")
			return
		}
	} else {
		in.Subject = ""
	}
	invID := "i" + newID(6)
	token, hash, salt, err := s.mintInviteToken(invID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	inv := Invitation{ID: invID, DocumentID: d.ID, Kind: in.Kind, Subject: in.Subject, Role: in.Role, Hint: token[len(token)-4:], CreatedBy: p.Username, ExpiresAt: exp}
	if err := s.store.CreateInvitation(ctx, inv, hash, salt, in.Kind == "user" && in.SendEmail, now); err != nil {
		s.createErr(w, err, now)
		return
	}
	_ = s.store.Audit(ctx, p.Actor, d.ID, "invite-create", "ok", map[string]any{"invitationId": inv.ID, "kind": in.Kind, "subject": in.Subject, "role": in.Role, "expiresAt": exp})
	inv.Status, inv.CreatedAt = "pending", now.UTC()
	if in.Kind == "user" && in.SendEmail {
		sent := now.UTC()
		inv.EmailSentAt = &sent // reserved now; the gateway releases it when the mail cannot be sent
	}
	inv.fillState(now)
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": inv, "token": token, "documentTitle": d.Title})
}

// createErr maps the store's rule violations to answers.
func (s *Server) createErr(w http.ResponseWriter, err error, now time.Time) {
	var pend errAlreadyPending
	switch {
	case errors.Is(err, errAlreadyHasAccess):
		writeErr(w, http.StatusConflict, "already_has_access", "this person already has this access or more")
	case errors.As(err, &pend):
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": "already_pending", "message": "an invitation for this person is already waiting: resend it instead"}, "invitationId": pend.id})
	case errors.Is(err, errEmailCooldown):
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":   map[string]string{"code": "email_cooldown", "message": "an invitation e-mail for this person and diagram was already sent today"},
			"retryAt": nextMidnightWIB(now).UTC()})
	case errors.Is(err, errEmailQuota):
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":   map[string]string{"code": "email_quota", "message": "too many invitation e-mails sent today (to this person or in total); copy the invitation link instead"},
			"retryAt": nextMidnightWIB(now).UTC()})
	case errors.Is(err, errTooManyInvites):
		writeErr(w, http.StatusConflict, "too_many_invitations", "cancel or revoke an invitation before creating another one")
	default:
		writeErr(w, http.StatusInternalServerError, "internal", "")
	}
}

func (s *Server) listInvitations(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, ok := s.ownerSide(w, r, p)
	if !ok {
		return
	}
	invs, err := s.store.Invitations(r.Context(), d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	now := time.Now()
	keyed := len(s.cfg.InviteTokenKey) >= 32
	for i := range invs {
		invs[i].fillState(now)
		invs[i].Recoverable = invs[i].Recoverable && keyed && invs[i].State == "pending"
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": invs})
}

// resendInvitation gives a waiting 'user' invitation a fresh token (the old link stops working) and expiry; the gateway mails it.
func (s *Server) resendInvitation(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, ok := s.ownerSide(w, r, p)
	if !ok {
		return
	}
	var in struct {
		ExpiresInDays *int `json:"expiresInDays"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &in, 1024) {
		return
	}
	now := time.Now()
	exp, valid := inviteExpiry(in.ExpiresInDays, now)
	if !valid {
		writeErr(w, http.StatusBadRequest, "bad_request", "expiresInDays 0..365 (0 = never)")
		return
	}
	token, hash, salt, err := s.mintInviteToken(r.PathValue("iid"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	inv, err := s.store.ResendInvitation(r.Context(), d.ID, r.PathValue("iid"), p.Username, hash, token[len(token)-4:], salt, exp, now)
	if err != nil {
		if errors.Is(err, errNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "invitation not found")
			return
		}
		s.createErr(w, err, now)
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "invite-resend", "ok", map[string]any{"invitationId": inv.ID, "subject": inv.Subject, "expiresAt": exp})
	inv.fillState(now)
	writeJSON(w, http.StatusOK, map[string]any{"invitation": inv, "token": token, "documentTitle": d.Title})
}

// inviteLink returns the link token of a live invitation again (owner side). It is computed, never read from storage; a link made
// before this feature, or under another key, cannot be shown (409) but keeps working.
func (s *Server) inviteLink(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, ok := s.ownerSide(w, r, p)
	if !ok {
		return
	}
	id := r.PathValue("iid")
	hash, salt, live, err := s.store.InviteTokenParts(r.Context(), d.ID, id)
	if err != nil || !live {
		writeErr(w, http.StatusNotFound, "not_found", "invitation not found")
		return
	}
	k := s.cfg.InviteTokenKey
	if len(k) < 32 || len(salt) == 0 {
		writeErr(w, http.StatusConflict, "link_not_recoverable", "this link cannot be shown again: create a new one")
		return
	}
	token := deriveInviteToken(k, id, salt)
	if subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(hash)) != 1 { // the key changed since: the link still works, it just cannot be recomputed
		writeErr(w, http.StatusConflict, "link_not_recoverable", "this link cannot be shown again: create a new one")
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, d.ID, "invite-link-view", "ok", map[string]string{"invitationId": id})
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

// deleteInvitation revokes a live invitation; on one that is already dead it removes it from the list (like MCP tokens).
func (s *Server) deleteInvitation(w http.ResponseWriter, r *http.Request, p *Principal) {
	d, ok := s.ownerSide(w, r, p)
	if !ok {
		return
	}
	id := r.PathValue("iid")
	if _, err := s.store.GetInvitation(r.Context(), d.ID, id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "invitation not found")
		return
	}
	if ok, err := s.store.RevokeInvitation(r.Context(), d.ID, id, p.Username); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	} else if ok {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "invite-revoke", "ok", map[string]string{"invitationId": id})
		writeJSON(w, http.StatusOK, map[string]any{"state": "revoked"})
		return
	}
	if ok, err := s.store.HideInvitation(r.Context(), d.ID, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	} else if ok {
		_ = s.store.Audit(r.Context(), p.Actor, d.ID, "invite-delete", "ok", map[string]string{"invitationId": id})
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": "deleted"})
}

// ---- handlers: the recipient's side ---------------------------------------------------------------------------------------------

type invitePreviewOut struct {
	Document   map[string]string `json:"document"`
	Role       string            `json:"role"`
	Inviter    map[string]string `json:"inviter"`
	ExpiresAt  *time.Time        `json:"expiresAt"`
	Kind       string            `json:"kind"`
	AlreadyHas string            `json:"alreadyHas,omitempty"`
}

// recipientGate: a signed-in human (not an AI actor, not an MCP token) within the per-person attempt limit.
func (s *Server) recipientGate(w http.ResponseWriter, p *Principal) bool {
	if p.IsAI || p.TokenDoc != "" {
		writeErr(w, http.StatusForbidden, "forbidden", "not available to AI actors")
		return false
	}
	if !s.inviteRate.allow("inv:" + p.Username) {
		w.Header().Set("Retry-After", "5")
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "terlalu banyak percobaan, coba lagi sebentar")
		return false
	}
	return true
}

// inviteErr maps the store errors to answers that never say whether a token exists (Indonesian: the recipient's page shows them).
func (s *Server) inviteErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInviteNotYours):
		writeErr(w, http.StatusForbidden, "invitation_not_for_you", "Undangan ini tidak ditujukan untuk akun Anda.")
	case errors.Is(err, errInviteOwner):
		writeErr(w, http.StatusConflict, "invitation_owner", "Anda adalah pemilik diagram ini.")
	case errors.Is(err, errInviteInvalid):
		writeErr(w, http.StatusNotFound, "invitation_invalid", "Undangan sudah tidak berlaku.")
	default:
		writeErr(w, http.StatusInternalServerError, "internal", "")
	}
}

func validInviteToken(t string) bool {
	return strings.HasPrefix(t, InvitePrefix) && len(t) == len(InvitePrefix)+43
}

func (s *Server) previewInvitation(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.recipientGate(w, p) {
		return
	}
	tok := r.PathValue("token")
	if !validInviteToken(tok) {
		s.inviteErr(w, errInviteInvalid)
		return
	}
	pv, err := s.store.PreviewInvitation(r.Context(), tok, p.Username)
	if err != nil {
		s.inviteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, invitePreviewOut{
		Document: map[string]string{"title": pv.DocTitle}, Role: pv.Role, Inviter: map[string]string{"name": pv.InviterOf, "username": pv.CreatedBy},
		ExpiresAt: pv.ExpiresAt, Kind: pv.Kind, AlreadyHas: pv.AlreadyHas})
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.recipientGate(w, p) {
		return
	}
	tok := r.PathValue("token")
	if !validInviteToken(tok) {
		s.inviteErr(w, errInviteInvalid)
		return
	}
	pv, role, err := s.store.AcceptInvitation(r.Context(), tok, p.Username)
	if err != nil {
		_ = s.store.Audit(r.Context(), p.Actor, "", "invite-accept", "denied", map[string]string{"reason": err.Error()})
		s.inviteErr(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, pv.DocumentID, "invite-accept", "ok", map[string]any{"invitationId": pv.ID, "kind": pv.Kind, "role": role})
	s.notifyPerm(pv.DocumentID)
	writeJSON(w, http.StatusOK, map[string]any{"documentId": pv.DocumentID, "role": role})
}

func (s *Server) declineInvitation(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.recipientGate(w, p) {
		return
	}
	tok := r.PathValue("token")
	if !validInviteToken(tok) {
		s.inviteErr(w, errInviteInvalid)
		return
	}
	pv, err := s.store.DeclineInvitation(r.Context(), tok, p.Username)
	if err != nil {
		s.inviteErr(w, err)
		return
	}
	_ = s.store.Audit(r.Context(), p.Actor, pv.DocumentID, "invite-decline", "ok", map[string]any{"invitationId": pv.ID, "kind": pv.Kind})
	writeJSON(w, http.StatusOK, map[string]any{"declined": true})
}

// ---- gateway-only endpoints (service token) -------------------------------------------------------------------------------------

// internalContact answers GET /internal/contacts/{username}: the e-mail the platform knows for that account.
func (s *Server) internalContact(w http.ResponseWriter, r *http.Request) {
	if !s.authService(r) {
		writeErr(w, http.StatusForbidden, "forbidden", "service token required")
		return
	}
	c, err := s.store.ContactOf(r.Context(), strings.ToLower(r.PathValue("username")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// internalEmailFailed gives the day's e-mail back: the gateway could not hand the mail to the MTA.
func (s *Server) internalEmailFailed(w http.ResponseWriter, r *http.Request) {
	if !s.authService(r) {
		writeErr(w, http.StatusForbidden, "forbidden", "service token required")
		return
	}
	id := r.PathValue("iid")
	ok, err := s.store.ReleaseEmail(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	_ = s.store.Audit(r.Context(), model.Actor{ID: "gateway", Type: "service"}, "", "invite-email", "failed", map[string]any{"invitationId": id, "released": ok})
	writeJSON(w, http.StatusOK, map[string]any{"released": ok})
}

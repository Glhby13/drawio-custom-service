package platform

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Glhby13/drawio-custom-service/internal/config"
	"github.com/Glhby13/drawio-custom-service/internal/identity"
	"github.com/Glhby13/drawio-custom-service/internal/model"
)

// Audience is the `aud` every internal token for this service carries.
const Audience = "platform"

// Server wires the platform.
type Server struct {
	cfg     config.Platform
	log     *slog.Logger
	store   *Store
	blob    *Blob
	rdb     *redis.Client
	verify  *identity.VerifierSet
	hub     *Hub
	replica string
	seen    sync.Map // username -> last upsert time
	rate    *rateLimiter
	local   localCache   // 15 s cache of local-account state used by authn
	actRate *rateLimiter // live presence reports per session (best effort, dropped when exceeded)
	started time.Time
}

// Principal is the verified caller.
type Principal struct {
	Sub      string
	Username string
	Name     string
	Email    string
	Roles    []string
	Actor    model.Actor
	IsAdmin  bool
	IsAI     bool
	Session  string
	Idp      string // "local" (DrawSpace account), "sso" or "" (AI actors)
}

// New builds the server; the caller owns pool/blob/redis lifetime.
func New(cfg config.Platform, log *slog.Logger, store *Store, blob *Blob, rdb *redis.Client) *Server {
	vs := identity.NewVerifierSet()
	for _, k := range cfg.Trusted {
		vs.Add(k.Kid, k.Pub, k.Issuer)
	}
	s := &Server{cfg: cfg, log: log, store: store, blob: blob, rdb: rdb, verify: vs, replica: newID(5), started: time.Now(), rate: newRateLimiter(40, 80), actRate: newRateLimiter(30, 60)}
	s.hub = newHub(s)
	return s
}

// Start launches background loops (cross-replica fan-out, janitor).
func (s *Server) Start(ctx context.Context) {
	go s.hub.janitor(ctx)
	go s.subscribeLoop(ctx)
}

// ---- cross-replica messages -------------------------------------------------------------------------------

type ctlMsg struct {
	Origin     string       `json:"origin"`
	Kind       string       `json:"kind"` // ops | resync | perm | presence | act | close
	Ops        []model.Op   `json:"ops,omitempty"`
	Hash       string       `json:"hash,omitempty"`
	Overridden []Overridden `json:"overridden,omitempty"`
	Act        *ActMsg      `json:"act,omitempty"`
}

func chanFor(docID string) string { return "drawio:doc:" + docID }

func (s *Server) publish(docID string, m ctlMsg) {
	raw, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.rdb.Publish(ctx, chanFor(docID), raw).Err(); err != nil {
		s.log.Warn("redis publish failed", "doc", docID, "err", err.Error())
	}
}

func (s *Server) subscribeLoop(ctx context.Context) {
	for ctx.Err() == nil {
		ps := s.rdb.PSubscribe(ctx, "drawio:doc:*")
		ch := ps.Channel()
		for msg := range ch {
			docID := strings.TrimPrefix(msg.Channel, "drawio:doc:")
			var m ctlMsg
			if json.Unmarshal([]byte(msg.Payload), &m) != nil || m.Origin == s.replica {
				continue
			}
			s.onRemote(docID, m)
		}
		_ = ps.Close()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) onRemote(docID string, m ctlMsg) {
	dh := s.hub.existing(docID)
	if dh == nil {
		return
	}
	dh.mu.Lock()
	defer dh.mu.Unlock()
	switch m.Kind {
	case "ops":
		if dh.doc == nil || len(m.Ops) == 0 {
			return
		}
		first, last := m.Ops[0].Seq, m.Ops[len(m.Ops)-1].Seq
		if last <= dh.doc.Seq {
			return
		}
		if first != dh.doc.Seq+1 { // gap: reload lazily, tell sessions to resync
			dh.doc = nil
			dh.broadcastResync("gap")
			return
		}
		for i := range m.Ops {
			dh.doc.Apply(&m.Ops[i])
			dh.doc.Seq = m.Ops[i].Seq
		}
		dh.broadcastOps(m.Ops, m.Hash, m.Overridden)
	case "resync":
		dh.doc = nil
		dh.broadcastResync("remote")
	case "perm":
		dh.reauthorize(s)
	case "presence":
		dh.broadcastPresence(s)
	case "act":
		if m.Act != nil {
			dh.setAct(m.Act)
			dh.broadcastAct(m.Act, nil)
		}
	}
}

// ---- authn -----------------------------------------------------------------------------------------------

func (s *Server) authn(r *http.Request) (*Principal, error) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		return nil, identity.ErrInvalid
	}
	c, err := s.verify.Verify(tok, Audience)
	if err != nil {
		return nil, err
	}
	username := c.Username
	if username == "" {
		username = c.Subject
	}
	p := &Principal{Sub: c.Subject, Username: username, Name: c.Name, Roles: c.Roles, Session: c.SessionID}
	for _, r := range c.Roles {
		if r == s.cfg.AdminRole {
			p.IsAdmin = true
		}
	}
	p.Idp = c.Idp
	if c.Idp == "local" {
		// a local account: only the gateway may vouch for it, and the database (not the token) decides role and status
		if c.Issuer != "drawio-gateway" || !strings.HasPrefix(c.Subject, "local:") {
			return nil, errNoAppRole
		}
		st, err := s.localState(r.Context(), username)
		if err != nil {
			return nil, err
		}
		if !st.exists || st.disabled {
			return nil, errLocalNotAllowed
		}
		p.IsAdmin = st.role == "admin"
	} else {
		// everybody else must not share a username with a local account (it would inherit that account's diagrams and shares)
		st, err := s.localState(r.Context(), username)
		if err != nil {
			return nil, err
		}
		if st.exists {
			return nil, errLocalCollision
		}
		if !p.IsAdmin && !hasRole(c.Roles, s.cfg.UserRole) {
			return nil, errNoAppRole
		}
	}
	p.IsAI = c.ActorType == identity.ActorAI
	p.Actor = model.Actor{ID: username, Type: c.ActorType, Name: c.Name, OnBehalfOf: c.OnBehalfOf, Agent: c.Agent, Model: c.Model}
	if p.Actor.Type == "" {
		p.Actor.Type = identity.ActorHuman
	}
	if p.IsAI {
		p.IsAdmin = false // an AI actor is never an application admin
	}
	// remember the principal for the share picker (at most once a minute per user)
	if t, ok := s.seen.Load(p.Sub); !ok || time.Since(t.(time.Time)) > time.Minute {
		s.seen.Store(p.Sub, time.Now())
		kind := "human"
		if p.IsAI {
			kind = "ai"
		}
		go func() { //nolint:gosec // best-effort write that must outlive the request
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.store.UpsertUser(ctx, p.Sub, p.Username, p.Name, p.Email, kind); err != nil {
				s.log.Warn("user upsert failed", "err", err.Error())
			}
		}()
	}
	return p, nil
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

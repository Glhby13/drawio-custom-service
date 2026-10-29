package platform

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

var errNoAppRole = errors.New("principal has no drawio application role")

// Roles ordered by power. "admin" (application admin) is a flag on the principal, not a document role.
const (
	RoleNone      = ""
	RoleViewer    = "viewer"
	RoleCommenter = "commenter"
	RoleEditor    = "editor"
	RoleOwner     = "owner"
)

func rank(role string) int {
	switch role {
	case RoleViewer:
		return 1
	case RoleCommenter:
		return 2
	case RoleEditor:
		return 3
	case RoleOwner:
		return 4
	}
	return 0
}

func minRole(a, b string) string {
	if rank(a) <= rank(b) {
		return a
	}
	return b
}

func maxRole(a, b string) string {
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

// Action is what a caller wants to do with a document; the single PDP below answers for REST, WebSocket and MCP.
type Action string

// The actions known to the PDP.
const (
	ActRead     Action = "read"
	ActComment  Action = "comment"
	ActEdit     Action = "edit"
	ActShare    Action = "share"
	ActDelete   Action = "delete"
	ActRename   Action = "rename"
	ActRestore  Action = "restore"
	ActExport   Action = "export"
	ActPolicy   Action = "policy"
	ActAudit    Action = "audit"
	ActRevert   Action = "revert"
	ActVersions Action = "version"
)

// docRole computes the principal's effective document role (the heart of the PDP, ADR-0004/0009):
//   - human: owner, else the best of their own share and the "*" (everyone) share;
//   - AI actor: the minimum of its own explicit share and, when acting on behalf of a user, that user's role; capped at
//     editor, and only when the document's AI policy allows AI at all.
func (s *Server) docRole(ctx context.Context, p *Principal, d *DocMeta) (string, error) {
	if p.IsAI {
		if !d.AI.Allowed {
			return RoleNone, nil
		}
		if p.TokenDoc != "" { // a per-diagram MCP token: this one document, at most the token's role, only while its creator still owns it
			if d.ID != p.TokenDoc || d.Owner != p.Actor.OnBehalfOf {
				return RoleNone, nil
			}
			return minRole(minRole(p.TokenRole, RoleEditor), RoleOwner), nil
		}
		own, err := s.sharedRole(ctx, d, p.Username, false)
		if err != nil {
			return RoleNone, err
		}
		role := minRole(own, RoleEditor)
		if obo := p.Actor.OnBehalfOf; obo != "" {
			delegated, err := s.sharedRole(ctx, d, obo, true)
			if err != nil {
				return RoleNone, err
			}
			role = minRole(role, delegated)
		}
		return role, nil
	}
	return s.sharedRole(ctx, d, p.Username, true)
}

func (s *Server) sharedRole(ctx context.Context, d *DocMeta, subject string, allowEveryone bool) (string, error) {
	if d.Owner == subject {
		return RoleOwner, nil
	}
	role, err := s.store.RoleOf(ctx, d.ID, subject)
	if err != nil {
		return RoleNone, err
	}
	if allowEveryone {
		every, err := s.store.RoleOf(ctx, d.ID, "*")
		if err != nil {
			return RoleNone, err
		}
		role = maxRole(role, every)
	}
	return role, nil
}

// can answers the PDP question. An application admin may read, share, delete, restore, export, set policy and audit any
// document but never edits content (editing needs an editor/owner share like everybody else).
func (s *Server) can(p *Principal, role string, a Action) bool {
	switch a {
	case ActRead, ActExport:
		return rank(role) >= 1 || p.IsAdmin
	case ActComment:
		return rank(role) >= rank(RoleCommenter)
	case ActEdit, ActRevert:
		return rank(role) >= rank(RoleEditor)
	case ActVersions, ActRestore, ActRename:
		return rank(role) >= rank(RoleEditor) || (p.IsAdmin && a != ActVersions)
	case ActShare, ActDelete, ActPolicy, ActAudit:
		return (role == RoleOwner && !p.IsAI) || p.IsAdmin
	}
	return false
}

// ---- tiny token-bucket rate limiter (per key) ----------------------------------------------------------------

type rateLimiter struct {
	mu     sync.Mutex
	rps    float64
	burst  float64
	bucket map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newRateLimiter(rps, burst float64) *rateLimiter {
	return &rateLimiter{rps: rps, burst: burst, bucket: map[string]*bucket{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.bucket[key]
	if b == nil {
		if len(l.bucket) > 20000 {
			l.bucket = map[string]*bucket{}
		}
		b = &bucket{tokens: l.burst, at: now}
		l.bucket[key] = b
	}
	b.tokens += now.Sub(b.at).Seconds() * l.rps
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// aiQuota enforces the per-document ops-per-minute limit for AI actors (Redis counter, shared by replicas).
func (s *Server) aiQuota(ctx context.Context, d *DocMeta, p *Principal, n int) bool {
	if !p.IsAI {
		return true
	}
	key := "drawio:rl:ai:" + d.ID + ":" + p.Username + ":" + strconv.FormatInt(time.Now().Unix()/60, 10)
	c, err := s.rdb.IncrBy(ctx, key, int64(n)).Result()
	if err != nil {
		return false // fail closed for AI
	}
	if c == int64(n) {
		s.rdb.Expire(ctx, key, 90*time.Second)
	}
	return c <= int64(d.AI.MaxOpsPerMin)
}

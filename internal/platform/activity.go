package platform

import (
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Live presence (ephemeral, best effort; SPIKE-003). What a collaborator is doing *right now* - selecting, editing a
// label, dragging, resizing - travels over the same WebSocket and Redis pub/sub as the roster, but it is NOT document
// state: it is never written to PostgreSQL, never enters the operation log, history, versions, audit or MCP, is not a lock
// and is not an authorisation source. Every entry expires on its own (actTTL) unless the browser keeps sending heartbeats.
// Presence informs; the semantic operation layer resolves.

const (
	actTTL       = 8 * time.Second // a heartbeat from the browser is expected every ~3 s while something is active
	aiActTTL     = 4 * time.Second
	maxActCells  = 50
	maxDraftRune = 500
	maxCoord     = 1e7
)

// Activity states a browser may report.
const (
	ActIdle       = "idle"
	ActSelecting  = "selecting"
	ActEditing    = "editing"
	ActDragging   = "dragging"
	ActResizing   = "resizing"
	ActConnecting = "connecting"
	ActApplying   = "applying" // AI actors only, set by the server while it applies a batch
	ActGone       = "gone"     // server -> clients: the collaborator left
)

// ActPreview is the transient geometry of a drag (dx, dy) or a resize (x, y, w, h), in diagram units of the page.
type ActPreview struct {
	Dx *float64 `json:"dx,omitempty"`
	Dy *float64 `json:"dy,omitempty"`
	X  *float64 `json:"x,omitempty"`
	Y  *float64 `json:"y,omitempty"`
	W  *float64 `json:"w,omitempty"`
	H  *float64 `json:"h,omitempty"`
}

// ActIn is what a browser sends ({"t":"act","act":{...}}).
type ActIn struct {
	State   string      `json:"state"`
	Page    string      `json:"page,omitempty"`
	Cells   []string    `json:"cells,omitempty"`
	Preview *ActPreview `json:"preview,omitempty"`
	Draft   *string     `json:"draft,omitempty"`
}

// ActMsg is what every other collaborator receives. Identity fields are set by the server, never by the client.
type ActMsg struct {
	SID     string      `json:"sid"`
	User    string      `json:"user,omitempty"`
	Name    string      `json:"name,omitempty"`
	Type    string      `json:"type,omitempty"`
	Agent   string      `json:"agent,omitempty"`
	State   string      `json:"state"`
	Page    string      `json:"page,omitempty"`
	Cells   []string    `json:"cells,omitempty"`
	Preview *ActPreview `json:"preview,omitempty"`
	Draft   *string     `json:"draft,omitempty"`
	Exp     int64       `json:"exp,omitempty"` // unix milliseconds
}

var actIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,128}$`)

func finite(p *float64) *float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) || math.Abs(*p) > maxCoord {
		return nil
	}
	v := math.Round(*p*100) / 100
	return &v
}

// sanitizeAct validates a browser report against the sender's role and returns the normalised activity, or false when
// it must be dropped. Viewers and commenters can only show selections; edit-like states need editor rights (the server
// still re-checks every real operation, this only keeps misleading "editing" badges off other people's screens).
func sanitizeAct(role string, in *ActIn) (ActIn, bool) {
	if in == nil {
		return ActIn{}, false
	}
	out := ActIn{State: in.State}
	switch in.State {
	case ActIdle:
		return ActIn{State: ActIdle}, true
	case ActSelecting:
	case ActEditing, ActDragging, ActResizing, ActConnecting:
		if rank(role) < rank(RoleEditor) {
			out.State = ActSelecting
		}
	default:
		return ActIn{}, false
	}
	if len(in.Page) > 128 {
		return ActIn{}, false
	}
	out.Page = in.Page
	for _, c := range in.Cells {
		if len(out.Cells) >= maxActCells {
			break
		}
		if actIDRe.MatchString(c) {
			out.Cells = append(out.Cells, c)
		}
	}
	if len(out.Cells) == 0 && out.State != ActConnecting {
		return ActIn{State: ActIdle}, true
	}
	if out.State == in.State && in.Preview != nil {
		p := ActPreview{}
		switch in.State {
		case ActDragging:
			p.Dx, p.Dy = finite(in.Preview.Dx), finite(in.Preview.Dy)
		case ActResizing:
			p.X, p.Y, p.W, p.H = finite(in.Preview.X), finite(in.Preview.Y), finite(in.Preview.W), finite(in.Preview.H)
		}
		if p != (ActPreview{}) {
			out.Preview = &p
		}
	}
	if out.State == ActEditing && in.Draft != nil {
		d := *in.Draft
		if !utf8.ValidString(d) {
			d = strings.ToValidUTF8(d, "")
		}
		if utf8.RuneCountInString(d) > maxDraftRune {
			d = string([]rune(d)[:maxDraftRune])
		}
		out.Draft = &d
	}
	return out, true
}

// ---- hub integration (callers hold dh.mu) ---------------------------------------------------------------------

func (dh *docHandle) setAct(a *ActMsg) {
	if dh.acts == nil {
		dh.acts = map[string]*ActMsg{}
	}
	now := time.Now().UnixMilli()
	for sid, o := range dh.acts { // lazy purge of expired entries
		if o.Exp != 0 && o.Exp < now {
			delete(dh.acts, sid)
		}
	}
	if a.State == ActIdle || a.State == ActGone {
		delete(dh.acts, a.SID)
		return
	}
	dh.acts[a.SID] = a
}

func (dh *docHandle) broadcastAct(a *ActMsg, except *Session) {
	for se := range dh.sessions {
		if se != except {
			se.enqueue(Out{T: "act", Act: a})
		}
	}
}

// actSnapshot is sent to a session that just joined.
func (dh *docHandle) actSnapshot(except string) []*ActMsg {
	now := time.Now().UnixMilli()
	var out []*ActMsg
	for sid, a := range dh.acts {
		if sid != except && (a.Exp == 0 || a.Exp >= now) {
			out = append(out, a)
		}
	}
	return out
}

// handleAct processes one report from a browser session.
func (se *Session) handleAct(in *ActIn) {
	s := se.s
	clean, ok := sanitizeAct(se.getRole(), in)
	if !ok || se.dh == nil {
		return
	}
	if clean.State != ActIdle && !s.actRate.allow("act:"+se.id) {
		return // best effort: drop, the next heartbeat or change repairs it
	}
	a := &ActMsg{SID: se.id, User: se.p.Username, Name: se.p.Name, Type: se.p.Actor.Type, Agent: se.p.Actor.Agent,
		State: clean.State, Page: clean.Page, Cells: clean.Cells, Preview: clean.Preview, Draft: clean.Draft}
	if a.State != ActIdle {
		a.Exp = time.Now().Add(actTTL).UnixMilli()
	}
	dh := se.dh
	dh.mu.Lock()
	dh.setAct(a)
	dh.broadcastAct(a, se)
	dh.mu.Unlock()
	s.publish(dh.id, ctlMsg{Origin: s.replica, Kind: "act", Act: a})
}

// announceAI shows an AI actor that applies a batch through the REST API (MCP). It is truthful: the state is "applying" for the
// cells the batch touched, for a few seconds, and never pretends to be typing.
func (s *Server) announceAI(docID string, p *Principal, cells []string) {
	if !p.IsAI {
		return
	}
	if len(cells) > maxActCells {
		cells = cells[:maxActCells]
	}
	a := &ActMsg{SID: "ai:" + p.Username, User: p.Username, Name: p.Actor.Name, Type: p.Actor.Type, Agent: p.Actor.Agent,
		State: ActApplying, Cells: cells, Exp: time.Now().Add(aiActTTL).UnixMilli()}
	if dh := s.hub.existing(docID); dh != nil {
		dh.mu.Lock()
		dh.setAct(a)
		dh.broadcastAct(a, nil)
		dh.mu.Unlock()
	}
	s.publish(docID, ctlMsg{Origin: s.replica, Kind: "act", Act: a})
}

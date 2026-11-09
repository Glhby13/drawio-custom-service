package edge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Glhby13/drawio-custom-service/internal/session"
)

// directoryHit is one share-picker suggestion; the JSON shape matches the platform's own /api/v1/users.
type directoryHit struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	HasEmail bool   `json:"hasEmail"` // an address is on record; the address itself never reaches the browser
}

// SearchDirectory asks Keycloak (the realm federates the corporate directory) for enabled users matching q,
// so a diagram can be shared with someone who has never signed in to DrawSpace.
func (o *OIDC) SearchDirectory(ctx context.Context, q string) ([]directoryHit, error) {
	var reps []struct {
		Username  string `json:"username"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Email     string `json:"email"`
		Enabled   bool   `json:"enabled"`
	}
	path := "/users?briefRepresentation=true&max=20&search=" + url.QueryEscape(q)
	if err := o.adminDo(ctx, http.MethodGet, path, nil, &reps); err != nil {
		return nil, err
	}
	out := make([]directoryHit, 0, len(reps))
	for _, r := range reps {
		if !r.Enabled || r.Username == "" || strings.HasPrefix(r.Username, "service-account-") {
			continue
		}
		out = append(out, directoryHit{Username: strings.ToLower(r.Username), Name: strings.TrimSpace(r.FirstName + " " + r.LastName), Kind: "human", HasEmail: validEmail(strings.TrimSpace(r.Email))})
	}
	return out, nil
}

// searchUsers answers GET /api/v1/users: the platform's own hits (local accounts, AI actors, people already seen)
// merged with the Keycloak directory. A Keycloak failure never breaks the picker: the platform answer is used alone.
func (s *Server) searchUsers(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	rec := httptest.NewRecorder()
	s.platformAPI(rec, r, sess) // validated, signed and rate-limited like any other platform call
	if rec.Code != http.StatusOK {
		copyRecorded(w, rec)
		return
	}
	var base struct {
		Users []directoryHit `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &base); err != nil {
		copyRecorded(w, rec)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 64 {
		q = q[:64]
	}
	if q != "" && s.oidc != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		hits, err := s.oidc.SearchDirectory(ctx, q)
		if err != nil {
			s.log.Warn("directory search failed", "request_id", RequestID(r.Context()), "err", err.Error())
		}
		seen := map[string]int{}
		for i, u := range base.Users {
			seen[u.Username] = i
		}
		for _, h := range hits {
			if i, ok := seen[h.Username]; ok {
				base.Users[i].HasEmail = base.Users[i].HasEmail || h.HasEmail
				continue
			}
			seen[h.Username] = len(base.Users)
			base.Users = append(base.Users, h)
		}
		sort.SliceStable(base.Users, func(i, j int) bool { return base.Users[i].Username < base.Users[j].Username })
		if len(base.Users) > 30 {
			base.Users = base.Users[:30]
		}
	}
	if base.Users == nil {
		base.Users = []directoryHit{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"users": base.Users, "canEmail": s.mailer != nil})
}

func copyRecorded(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// LookupContact returns the e-mail and display name Keycloak has for exactly this username ("" when none or disabled).
func (o *OIDC) LookupContact(ctx context.Context, username string) (email, name string, err error) {
	var reps []struct {
		Username  string `json:"username"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Email     string `json:"email"`
		Enabled   bool   `json:"enabled"`
	}
	if err := o.adminDo(ctx, http.MethodGet, "/users?briefRepresentation=true&exact=true&max=2&username="+url.QueryEscape(username), nil, &reps); err != nil {
		return "", "", err
	}
	for _, r := range reps {
		if r.Enabled && strings.EqualFold(r.Username, username) {
			return strings.TrimSpace(r.Email), strings.TrimSpace(r.FirstName + " " + r.LastName), nil
		}
	}
	return "", "", nil
}

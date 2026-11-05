package platform

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Folders are a personal view of the list (ADR-0020): humans only, never part of an AI token's or actor's reach.
func (s *Server) humanOnly(w http.ResponseWriter, p *Principal) bool {
	if p.IsAI || p.TokenDoc != "" {
		writeErr(w, http.StatusForbidden, "forbidden", "folders are for signed-in people")
		return false
	}
	return true
}

func (s *Server) folderErr(w http.ResponseWriter, err error) {
	var fe *folderError
	switch {
	case errors.As(err, &fe):
		code := http.StatusBadRequest
		switch fe.code {
		case "conflict":
			code = http.StatusConflict
		case "limit":
			code = http.StatusUnprocessableEntity
		}
		writeErr(w, code, fe.code, fe.msg)
	case errors.Is(err, errNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "folder not found")
	default:
		s.log.Error("folders", "err", err.Error())
		writeErr(w, http.StatusInternalServerError, "internal", "")
	}
}

func (s *Server) listFolders(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.humanOnly(w, p) {
		return
	}
	fs, err := s.store.ListFolders(r.Context(), p.Username)
	if err != nil {
		s.folderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": fs})
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.humanOnly(w, p) {
		return
	}
	var in struct {
		Name     string  `json:"name"`
		ParentID *string `json:"parentId"`
	}
	if !readJSON(w, r, &in, 4096) {
		return
	}
	f, err := s.store.CreateFolder(r.Context(), p.Username, in.ParentID, in.Name)
	if err != nil {
		s.folderErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"folder": f})
}

func (s *Server) patchFolder(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.humanOnly(w, p) {
		return
	}
	var raw map[string]json.RawMessage
	if !readJSON(w, r, &raw, 4096) {
		return
	}
	var name *string
	if v, ok := raw["name"]; ok {
		var n string
		if json.Unmarshal(v, &n) != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "name must be a string")
			return
		}
		name = &n
	}
	var moveTo **string
	if v, ok := raw["parentId"]; ok { // present (even null) = move; null = to the root
		var np *string
		if json.Unmarshal(v, &np) != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "parentId must be a string or null")
			return
		}
		moveTo = &np
	}
	if name == nil && moveTo == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "name or parentId required")
		return
	}
	f, err := s.store.UpdateFolder(r.Context(), p.Username, r.PathValue("id"), name, moveTo)
	if err != nil {
		s.folderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folder": f})
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.humanOnly(w, p) {
		return
	}
	if err := s.store.DeleteFolder(r.Context(), p.Username, r.PathValue("id")); err != nil {
		s.folderErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// putPlacement: where the caller keeps a diagram they can read (own or shared). Only the caller's view changes.
func (s *Server) putPlacement(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !s.humanOnly(w, p) {
		return
	}
	d, _, ok := s.access(w, r, p, ActRead)
	if !ok {
		return
	}
	var in struct {
		FolderID *string `json:"folderId"`
		Inbox    bool    `json:"inbox"`
	}
	if !readJSON(w, r, &in, 4096) {
		return
	}
	if err := s.store.Place(r.Context(), p.Username, d.ID, in.FolderID, in.Inbox, d.Owner == p.Username); err != nil {
		s.folderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folderId": in.FolderID, "inbox": in.Inbox})
}

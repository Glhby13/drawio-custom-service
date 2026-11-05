package platform

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	maxFolderDepth = 8   // levels below the root
	maxFolders     = 500 // per user
	maxFolderName  = 100
)

// Folder is one node of a user's personal folder tree.
type Folder struct {
	ID        string    `json:"id"`
	ParentID  *string   `json:"parentId"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// folderError is a refused folder change that the API reports as 400/409.
type folderError struct{ code, msg string }

func (e *folderError) Error() string { return e.msg }

var errFolderName = &folderError{"conflict", "a folder with that name already exists here"}

func cleanFolderName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len([]rune(n)) > maxFolderName {
		return "", &folderError{"bad_request", "folder name 1..100 characters"}
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return "", &folderError{"bad_request", "folder name must not contain control characters, / or \\"}
		}
	}
	return n, nil
}

// ListFolders returns the user's folders (own tree only).
func (s *Store) ListFolders(ctx context.Context, owner string) ([]Folder, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, parent_id, name, created_at FROM folders WHERE owner=$1 ORDER BY lower(name)`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// withFolders runs fn on the user's folders inside a transaction that serialises that user's folder changes.
func (s *Store) withFolders(ctx context.Context, owner string, fn func(tx pgx.Tx, parents map[string]*string) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('folders:' || $1))`, owner); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id, parent_id FROM folders WHERE owner=$1`, owner)
	if err != nil {
		return err
	}
	parents := map[string]*string{}
	for rows.Next() {
		var id string
		var p *string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return err
		}
		parents[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := fn(tx, parents); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// depthOf is the number of levels of the folder (a root folder = 1); 0 for "no folder".
func depthOf(parents map[string]*string, id *string) int {
	d := 0
	for id != nil && d <= maxFolderDepth+1 {
		d++
		id = parents[*id]
	}
	return d
}

// subtreeHeight is the number of levels the folder and everything below it take.
func subtreeHeight(parents map[string]*string, id string) int {
	h := 1
	for c, p := range parents {
		if p != nil && *p == id {
			if x := 1 + subtreeHeight(parents, c); x > h {
				h = x
			}
		}
	}
	return h
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// CreateFolder adds a folder under parent (nil = root).
func (s *Store) CreateFolder(ctx context.Context, owner string, parent *string, name string) (*Folder, error) {
	name, err := cleanFolderName(name)
	if err != nil {
		return nil, err
	}
	f := &Folder{ID: "f" + newID(8), ParentID: parent, Name: name}
	err = s.withFolders(ctx, owner, func(tx pgx.Tx, parents map[string]*string) error {
		if len(parents) >= maxFolders {
			return &folderError{"limit", "folder limit reached (500)"}
		}
		if parent != nil {
			if _, ok := parents[*parent]; !ok {
				return errNotFound
			}
			if depthOf(parents, parent) >= maxFolderDepth {
				return &folderError{"limit", "folders can be nested at most 8 levels deep"}
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO folders (id, owner, parent_id, name) VALUES ($1,$2,$3,$4) RETURNING created_at`, f.ID, owner, parent, name).Scan(&f.CreatedAt)
	})
	if isUniqueViolation(err) {
		return nil, errFolderName
	}
	return f, err
}

// UpdateFolder renames a folder and/or moves it under a new parent (moveTo != nil: *moveTo nil = root).
func (s *Store) UpdateFolder(ctx context.Context, owner, id string, name *string, moveTo **string) (*Folder, error) {
	var newName string
	if name != nil {
		n, err := cleanFolderName(*name)
		if err != nil {
			return nil, err
		}
		newName = n
	}
	var out Folder
	err := s.withFolders(ctx, owner, func(tx pgx.Tx, parents map[string]*string) error {
		cur, ok := parents[id]
		if !ok {
			return errNotFound
		}
		if moveTo != nil {
			np := *moveTo
			if np != nil {
				if _, ok := parents[*np]; !ok {
					return errNotFound
				}
				// a folder cannot go into itself or into one of its own descendants
				for x := np; x != nil; x = parents[*x] {
					if *x == id {
						return &folderError{"bad_request", "a folder cannot be moved into itself or its own subfolder"}
					}
				}
				if depthOf(parents, np)+subtreeHeight(parents, id) > maxFolderDepth {
					return &folderError{"limit", "folders can be nested at most 8 levels deep"}
				}
			}
			cur = np
		}
		if _, err := tx.Exec(ctx, `UPDATE folders SET parent_id=$3, name=COALESCE(NULLIF($4,''), name) WHERE id=$1 AND owner=$2`, id, owner, cur, newName); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id, parent_id, name, created_at FROM folders WHERE id=$1`, id).Scan(&out.ID, &out.ParentID, &out.Name, &out.CreatedAt)
	})
	if isUniqueViolation(err) {
		return nil, errFolderName
	}
	return &out, err
}

// DeleteFolder removes a folder; its subfolders and diagrams move up to its parent, nothing is lost.
func (s *Store) DeleteFolder(ctx context.Context, owner, id string) error {
	err := s.withFolders(ctx, owner, func(tx pgx.Tx, parents map[string]*string) error {
		parent, ok := parents[id]
		if !ok {
			return errNotFound
		}
		// the folder being deleted still holds its name at the parent level: park it under its id so an identically named child can move up
		if _, err := tx.Exec(ctx, `UPDATE folders SET name=id WHERE id=$1 AND owner=$2`, id, owner); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE folders SET parent_id=$3 WHERE owner=$1 AND parent_id=$2`, owner, id, parent); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE doc_placement SET folder_id=$3 WHERE owner=$1 AND folder_id=$2`, owner, id, parent); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM folders WHERE id=$1 AND owner=$2`, id, owner)
		return err
	})
	if isUniqueViolation(err) { // a moved-up child collides with a sibling name at the parent level
		return &folderError{"conflict", "moving the contents up would duplicate a folder name; rename one first"}
	}
	return err
}

// Place records where the user keeps a diagram. inbox = back to the default spot (root for own diagrams,
// "Shared with me" for shared ones); folderID nil = explicit root. It takes the same per-owner lock as the folder
// changes, so a concurrent DeleteFolder can neither orphan the row nor turn it into a 500.
func (s *Store) Place(ctx context.Context, owner, docID string, folderID *string, inbox, ownDoc bool) error {
	return s.withFolders(ctx, owner, func(tx pgx.Tx, parents map[string]*string) error {
		if inbox || (folderID == nil && ownDoc) {
			_, err := tx.Exec(ctx, `DELETE FROM doc_placement WHERE owner=$1 AND document_id=$2`, owner, docID)
			return err
		}
		if folderID != nil {
			if _, ok := parents[*folderID]; !ok {
				return errNotFound
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO doc_placement (owner, document_id, folder_id) VALUES ($1,$2,$3)
		  ON CONFLICT (owner, document_id) DO UPDATE SET folder_id=EXCLUDED.folder_id, placed_at=now()`, owner, docID, folderID)
		return err
	})
}

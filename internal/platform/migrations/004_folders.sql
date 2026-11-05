-- Personal folders (ADR-0020). Folders and placements are a per-user VIEW of the diagram list: they never change who may
-- open a diagram. A diagram without a placement row sits in the user's root (own diagrams) or in the virtual
-- "Shared with me" folder (diagrams shared with the user); a placement row with folder_id NULL is an explicit root.
CREATE TABLE folders (
  id         text PRIMARY KEY,
  owner      text NOT NULL,
  parent_id  text REFERENCES folders(id) ON DELETE CASCADE,
  name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX folders_owner ON folders (owner);
CREATE UNIQUE INDEX folders_unique_name ON folders (owner, COALESCE(parent_id, ''), lower(name));

CREATE TABLE doc_placement (
  owner       text NOT NULL,
  document_id text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  folder_id   text REFERENCES folders(id) ON DELETE CASCADE,
  placed_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (owner, document_id));
CREATE INDEX doc_placement_folder ON doc_placement (folder_id);

INSERT INTO schema_version (version, description) VALUES (4, 'personal folders and per-user diagram placement');

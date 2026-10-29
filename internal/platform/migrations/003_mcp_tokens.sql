-- Per-diagram MCP access tokens (ADR-0016). Only the SHA-256 of a token is stored; the token itself is shown once to the
-- owner who created it. A token opens exactly one document, with at most the role it was created with.
CREATE TABLE document_tokens (
  id           text PRIMARY KEY,
  document_id  text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  name         text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  token_hash   text NOT NULL UNIQUE,
  hint         text NOT NULL,
  role         text NOT NULL CHECK (role IN ('viewer','commenter','editor')),
  created_by   text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz,
  last_used_at timestamptz,
  revoked_at   timestamptz,
  revoked_by   text NOT NULL DEFAULT '');
CREATE INDEX document_tokens_doc ON document_tokens (document_id);

INSERT INTO schema_version (version, description) VALUES (3, 'per-diagram MCP access tokens');

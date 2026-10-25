-- drawio platform schema, version 1. Applied by the platform at start-up under an advisory lock
-- (lab); the office applies the same file by hand (docs/office-sync).
CREATE TABLE IF NOT EXISTS schema_version (
  version int PRIMARY KEY, description text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());

CREATE TABLE users (
  sub text PRIMARY KEY, username text NOT NULL UNIQUE, name text, email text, kind text NOT NULL DEFAULT 'human',
  first_seen timestamptz NOT NULL DEFAULT now(), last_seen timestamptz NOT NULL DEFAULT now());

CREATE TABLE documents (
  id text PRIMARY KEY, title text NOT NULL, owner text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  head_seq bigint NOT NULL DEFAULT 0, snapshot_seq bigint NOT NULL DEFAULT 0, deleted_at timestamptz,
  ai_policy jsonb NOT NULL DEFAULT '{"allowed":false,"maxOpsPerMin":300,"maxDelete":20}');
CREATE INDEX documents_owner ON documents (owner) WHERE deleted_at IS NULL;

CREATE TABLE permissions (
  document_id text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  subject text NOT NULL, role text NOT NULL CHECK (role IN ('viewer','commenter','editor')),
  granted_by text NOT NULL, granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (document_id, subject));
CREATE INDEX permissions_subject ON permissions (subject);

-- the operation log: RESOLVED ops, append-only, hash-partitioned by document (retention = drop partition)
CREATE TABLE ops (
  document_id text NOT NULL, seq bigint NOT NULL, op_id text NOT NULL, batch text NOT NULL,
  actor_id text NOT NULL, actor_type text NOT NULL, obo text, agent text, model text,
  type text NOT NULL, op jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (document_id, seq), UNIQUE (document_id, op_id)
) PARTITION BY HASH (document_id);
CREATE TABLE ops_p0 PARTITION OF ops FOR VALUES WITH (MODULUS 8, REMAINDER 0);
CREATE TABLE ops_p1 PARTITION OF ops FOR VALUES WITH (MODULUS 8, REMAINDER 1);
CREATE TABLE ops_p2 PARTITION OF ops FOR VALUES WITH (MODULUS 8, REMAINDER 2);
CREATE TABLE ops_p3 PARTITION OF ops FOR VALUES WITH (MODULUS 8, REMAINDER 3);
CREATE TABLE ops_p4 PARTITION OF ops FOR VALUES WITH (MODULUS 8, REMAINDER 4);
CREATE TABLE ops_p5 PARTITION OF ops FOR VALUES WITH (MODULUS 8, REMAINDER 5);
CREATE TABLE ops_p6 PARTITION OF ops FOR VALUES WITH (MODULUS 8, REMAINDER 6);
CREATE TABLE ops_p7 PARTITION OF ops FOR VALUES WITH (MODULUS 8, REMAINDER 7);
CREATE INDEX ops_batch ON ops (document_id, batch);

CREATE TABLE snapshots (
  document_id text NOT NULL, seq bigint NOT NULL, s3_key text NOT NULL, sha256 text NOT NULL, size int NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (document_id, seq));

CREATE TABLE versions (
  id text PRIMARY KEY, document_id text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  name text NOT NULL, seq bigint NOT NULL, created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX versions_doc ON versions (document_id, created_at DESC);

CREATE TABLE comments (
  id text PRIMARY KEY, document_id text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page text NOT NULL DEFAULT '', cell text NOT NULL DEFAULT '', author text NOT NULL, author_type text NOT NULL DEFAULT 'human',
  body text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), resolved boolean NOT NULL DEFAULT false);
CREATE INDEX comments_doc ON comments (document_id, created_at);

CREATE TABLE audit_events (
  id bigserial PRIMARY KEY, at timestamptz NOT NULL DEFAULT now(), actor_id text NOT NULL, actor_type text NOT NULL, obo text,
  document_id text, action text NOT NULL, outcome text NOT NULL, detail jsonb NOT NULL DEFAULT '{}');
CREATE INDEX audit_doc ON audit_events (document_id, at DESC);

INSERT INTO schema_version (version, description) VALUES (1, 'drawio platform: documents, permissions, ops log, snapshots, versions, comments, audit');

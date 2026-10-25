-- Local accounts (DrawSpace sign-in without SSO). SSO users keep living in the identity provider; this table only holds
-- accounts an application admin creates. Passwords are argon2id hashes, never plaintext.
CREATE TABLE local_users (
  username     text PRIMARY KEY CHECK (username ~ '^[a-z0-9][a-z0-9._-]{2,31}$'),
  display_name text NOT NULL DEFAULT '',
  email        text NOT NULL DEFAULT '',
  role         text NOT NULL CHECK (role IN ('user','admin')),
  pass_hash    text NOT NULL,
  disabled     boolean NOT NULL DEFAULT false,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   text NOT NULL DEFAULT '',
  last_login   timestamptz,
  pass_changed_at timestamptz NOT NULL DEFAULT now());

INSERT INTO schema_version (version, description) VALUES (2, 'local users (DrawSpace local sign-in)');

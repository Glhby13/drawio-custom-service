-- Invitation-based sharing (ADR-0021). Sharing no longer grants access directly: an invitation is accepted by its recipient,
-- and only then a row lands in `permissions`. Additive: no diagram, version, op, snapshot or comment is touched.
--
-- Also retires two things (their rows are COPIED to access_archive first, so nothing is lost and it can be undone):
--   * the "everyone" share (subject '*') - it made a diagram appear in every user's list;
--   * the "commenter" role - folded into viewer (shares and MCP tokens).
CREATE TABLE invitations (
  id            text PRIMARY KEY,
  document_id   text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  kind          text NOT NULL CHECK (kind IN ('user','link')),
  subject       text,                                              -- the one account a 'user' invitation is for
  role          text NOT NULL CHECK (role IN ('viewer','editor')),
  token_hash    text NOT NULL UNIQUE,                              -- SHA-256 of the token; the token itself is never stored
  hint          text NOT NULL,                                     -- last characters of the token, to tell links apart
  created_by    text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz,                                       -- NULL = never expires
  status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','declined','revoked')),
  decided_by    text,
  decided_at    timestamptz,
  email_sent_at timestamptz,                                       -- set only after the mail was handed to the MTA (feeds the daily limit)
  use_count     int NOT NULL DEFAULT 0,
  hidden_at     timestamptz,                                       -- removed from the owner's list; the row stays (daily limit, audit)
  CHECK ((kind = 'user' AND subject IS NOT NULL) OR (kind = 'link' AND subject IS NULL)));
CREATE INDEX invitations_document ON invitations (document_id);
CREATE INDEX invitations_recipient ON invitations (document_id, subject, created_by, email_sent_at) WHERE kind = 'user';
CREATE INDEX invitations_sender_mail ON invitations (created_by, email_sent_at) WHERE email_sent_at IS NOT NULL;

-- copies of access that this migration retires; restore with INSERT ... SELECT (see docs/office-sync)
CREATE TABLE access_archive (
  id          bigserial PRIMARY KEY,
  reason      text NOT NULL,                                       -- 'everyone' | 'commenter-share' | 'commenter-token'
  document_id text NOT NULL,
  subject     text NOT NULL,                                       -- username, '*', or the token id
  role        text NOT NULL,
  granted_by  text,
  granted_at  timestamptz,
  archived_at timestamptz NOT NULL DEFAULT now());

INSERT INTO access_archive (reason, document_id, subject, role, granted_by, granted_at)
  SELECT 'everyone', document_id, subject, role, granted_by, granted_at FROM permissions WHERE subject = '*';
DELETE FROM permissions WHERE subject = '*';

INSERT INTO access_archive (reason, document_id, subject, role, granted_by, granted_at)
  SELECT 'commenter-share', document_id, subject, role, granted_by, granted_at FROM permissions WHERE role = 'commenter';
UPDATE permissions SET role = 'viewer' WHERE role = 'commenter';

INSERT INTO access_archive (reason, document_id, subject, role, granted_by, granted_at)
  SELECT 'commenter-token', document_id, id, role, created_by, created_at FROM document_tokens WHERE role = 'commenter';
UPDATE document_tokens SET role = 'viewer' WHERE role = 'commenter';

INSERT INTO schema_version (version, description) VALUES (5, 'invitation-based sharing; retire everyone share and commenter role');

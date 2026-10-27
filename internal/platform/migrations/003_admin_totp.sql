-- Admin TOTP credentials for two-factor authentication.
CREATE TABLE IF NOT EXISTS admin_totp (
  username   text PRIMARY KEY,
  secret     text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now());

INSERT INTO schema_version (version, description) VALUES (3, 'admin totp credentials for two-factor authentication')
ON CONFLICT (version) DO NOTHING;

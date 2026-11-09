-- Invitation links can be copied again (ADR-0021 amendment). The token is no longer stored or lost: it is DERIVED,
-- token = "dsi_" + base64url(HMAC-SHA256(INVITE_TOKEN_KEY, "dsi1|" + id + "|" + token_salt)), so only someone who holds the server
-- key AND the database can compute it. The database still stores only the SHA-256 of the token (lookup) plus this salt.
-- Additive: old invitations keep working, they just have no salt and therefore cannot be shown again.
ALTER TABLE invitations ADD COLUMN token_salt bytea;
INSERT INTO schema_version (version, description) VALUES (6, 'invitation links can be shown again (derived tokens)');

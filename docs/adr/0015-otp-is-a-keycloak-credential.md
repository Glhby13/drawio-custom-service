# ADR-0015: One-time codes are Keycloak credentials, not gateway state

Author / Agent: Claude Code
Timestamp: 2026-10-28
Status: Accepted (lab). Supersedes the interim approach of keeping administrator TOTP secrets in `drawio_db.admin_totp`.

## Context
Administrators (client role `drawio-admin`) must use a one-time code. The interim implementation kept the secret in a
platform table and had the gateway verify it, while Keycloak verified codes only for users who had enrolled there. That
gave two places to enrol, reset and audit a second factor, and a code enrolled in one aplikasi did not count in another.

## Decision
1. The second factor lives only in Keycloak. The direct-grant flow `drawio-direct-grant` asks for the code when the user
   holds an OTP credential and the role `drawio-admin`; the gateway never verifies a code itself.
2. The gateway may *enrol*: when an administrator signs in without an OTP credential it shows a QR code, keeps the raw
   secret in Redis (5 minutes, single use, at most 5 wrong codes, counted by the account lock), and only after the first
   code is proved registers it with the Admin API (`PUT /users/{id}`, `credentials[type=otp]`, raw secret in
   `secretData.value`, base32 form in the QR). An existing OTP credential is never replaced.
3. The client's service account holds `view-users`, `query-users`, `manage-users`, `view-clients`, `query-clients`.
4. `admin_totp`, its migration, its endpoints and the local-account "Authenticator" dialog are removed. DrawSpace had
   not been deployed to the office, so the office receives the final schema (version 2) with no create-then-drop history.

## Consequences
- One OTP per user serves every Keycloak client whose flow asks for it; resetting it in Keycloak resets it everywhere.
- Enrolment needs `manage-users` on the service account; without it enrolment fails closed (no session, no credential).
- Known limit: Keycloak answers `invalid_grant` both for a wrong password and for a missing code, so an administrator
  who holds an OTP is recognisable by user name after the first attempt; failed attempts are rate limited and lock the account.

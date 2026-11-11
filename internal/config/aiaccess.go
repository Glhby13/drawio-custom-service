package config

import "slices"

// AIAccess says whether a person may use the AI/MCP feature (ADR-0022). It is the one rule the gateway (what the UI shows)
// and the platform (what the API allows) share:
//   - enabled is the global switch (AI_ENABLED); when it is off nobody may, whatever the roles are;
//   - requiredRole is the client role (AI_ACCESS_ROLE, e.g. "mcp-access") Keycloak gives to the members of a group; when it is
//     empty every signed-in user may (the lab behaviour); when it is set the person must hold exactly that role.
//
// Administrators get no shortcut: an administrator who needs AI joins the Keycloak group like anybody else.
func AIAccess(enabled bool, requiredRole string, userRoles []string) bool {
	if !enabled {
		return false
	}
	if requiredRole == "" {
		return true
	}
	return slices.Contains(userRoles, requiredRole)
}

package edge

import (
	"context"
	"testing"
)

// People who never signed in to DrawSpace must still be findable in the share picker: the directory is Keycloak.
func TestSearchDirectoryFindsUsersWhoNeverSignedIn(t *testing.T) {
	_, _, o := newOTPEnv(t, sari(),
		&kcUser{id: "k2", username: "budi.santoso"},
		&kcUser{id: "k3", username: "off-budi"},
		&kcUser{id: "k4", username: "service-account-budi"})
	hits, err := o.SearchDirectory(context.Background(), "budi")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Username != "budi.santoso" || hits[0].Name != "First budi.santoso" || hits[0].Kind != "human" {
		t.Fatalf("want only the enabled real user, got %+v", hits)
	}
}

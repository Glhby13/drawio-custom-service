package platform

import "testing"

func sp(s string) *string { return &s }

func TestFolderDepthAndHeight(t *testing.T) {
	parents := map[string]*string{"a": nil, "b": sp("a"), "c": sp("b"), "x": nil}
	if d := depthOf(parents, sp("c")); d != 3 {
		t.Fatalf("depth c = %d", d)
	}
	if d := depthOf(parents, nil); d != 0 {
		t.Fatalf("depth root = %d", d)
	}
	if h := subtreeHeight(parents, "a"); h != 3 {
		t.Fatalf("height a = %d", h)
	}
	if h := subtreeHeight(parents, "x"); h != 1 {
		t.Fatalf("height x = %d", h)
	}
}

func TestCleanFolderName(t *testing.T) {
	for _, bad := range []string{"", "   ", "a/b", `a\b`, "x\ny"} {
		if _, err := cleanFolderName(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	if n, err := cleanFolderName("  Proyek A  "); err != nil || n != "Proyek A" {
		t.Fatalf("got %q %v", n, err)
	}
	long := make([]rune, 101)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := cleanFolderName(string(long)); err == nil {
		t.Fatal("101 characters must be refused")
	}
}

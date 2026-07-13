package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"jamypg/internal/catalog"
)

func TestSanitizeProfileID(t *testing.T) {
	cases := map[string]string{
		"pg-meta":       "pg-meta",
		"a/b\\c":        "a_b_c",
		"../etc/passwd": ".._etc_passwd",
		"":              "_",
		"..":            "_",
	}
	for in, want := range cases {
		if got := sanitizeProfileID(in); got != want {
			t.Errorf("sanitizeProfileID(%q)=%q want %q", in, got, want)
		}
	}
}

func newPCServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{}
	s.setCatalog(&catalog.Catalog{DataDir: t.TempDir(), Tables: map[string]*catalog.Table{}})
	return s
}

func TestProfileCatalogWorkspaceLifecycle(t *testing.T) {
	s := newPCServer(t)
	dir := s.profileCatalogDir("pg-prod")
	if !filepath.IsAbs(dir) && dir == "" {
		t.Fatal("bad workspace dir")
	}

	// no workspace yet
	res := s.getProfileCatalog("pg-prod")
	if res["workspace"].(bool) {
		t.Fatalf("expected no workspace, got %+v", res)
	}

	// seed a workspace with a minimal physical model (as build_profile_catalog would)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	phys := []map[string]any{
		{"schema_name": "public", "table_name": "orders", "column_order": "1",
			"column_name": "id", "data_type": "BIGINT", "is_pk": "Y", "is_fk": "N", "description": "", "version": 1},
	}
	b, _ := json.MarshalIndent(phys, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta_physical_models.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// logical model is a required dataset; an empty one is valid
	if err := os.WriteFile(filepath.Join(dir, "meta_logical_models.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	// now it loads
	res = s.getProfileCatalog("pg-prod")
	if !res["workspace"].(bool) {
		t.Fatalf("workspace should exist: %+v", res)
	}
	if res["summary"] == nil || res["datasets"] == nil {
		t.Fatalf("summary/datasets missing: %+v", res)
	}

	// put a dataset (overrides) into the workspace, then read it back
	overrides := json.RawMessage(`{"columns":[{"table":"public.orders","column":"id","logical_name":"주문번호"}]}`)
	pr := s.putProfileDataset("pg-prod", "overrides", overrides)
	if !pr["applied"].(bool) {
		t.Fatalf("put failed: %+v", pr)
	}
	got := s.getProfileDataset("pg-prod", "overrides")
	if got["error"] != nil {
		t.Fatalf("get dataset failed: %v", got["error"])
	}
	raw, _ := got["content"].(json.RawMessage)
	if raw == nil || !contains(string(raw), "주문번호") {
		t.Fatalf("overrides content missing: %s", string(raw))
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

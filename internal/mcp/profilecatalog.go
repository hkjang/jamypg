package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jamypg/internal/catalog"
	"jamypg/internal/metasync"
)

// Per-profile catalog workspaces. Beyond the single global catalog (-data),
// each registered DB profile can own an independent set of metadata JSON files
// under <data>/profiles/<profileID>/. This lets operators view and manage
// catalog metadata per connected database, and build a profile's catalog
// straight from its live schema. The global catalog stays the NL2SQL default;
// these workspaces are managed/inspected via the tools below and can be
// promoted into the active catalog by pointing -data at one.

// profileCatalogDir returns the workspace directory for a profile id, kept
// under the main dataset dir so it works in standalone mode.
func (s *Server) profileCatalogDir(profileID string) string {
	return filepath.Join(s.cat().DataDir, "profiles", sanitizeProfileID(profileID))
}

// ensureWorkspaceScaffold creates the minimal required dataset files so
// catalog.Load succeeds on a fresh workspace (physical + logical models are
// required; an empty logical model is valid — tables simply have no logical
// names until managed).
func ensureWorkspaceScaffold(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range []string{"meta_physical_models.json", "meta_logical_models.json"} {
		p := filepath.Join(dir, f)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, []byte("[]\n"), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func sanitizeProfileID(id string) string {
	id = strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "_"
	}
	return out
}

// listProfileCatalogs reports, for every usable DB profile, whether it has a
// catalog workspace and its size.
func (s *Server) listProfileCatalogs(ctx context.Context) map[string]any {
	profs, err := s.usableProfiles(ctx)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := make([]map[string]any, 0, len(profs))
	for _, p := range profs {
		dir := s.profileCatalogDir(p.ID)
		entry := map[string]any{"profile": p.ID, "name": p.Name, "type": p.Type, "workspace": false}
		if fi, err := os.Stat(filepath.Join(dir, "meta_physical_models.json")); err == nil {
			entry["workspace"] = true
			entry["built_at"] = fi.ModTime().UTC().Format(time.RFC3339)
			if cat, err := catalog.Load(dir); err == nil {
				sum := cat.Summary()
				entry["tables"] = sum.TableCount
				entry["relations"] = sum.RelationCount
				q := cat.QualityReport()
				entry["quality_score"] = q.OverallScore
				entry["quality_grade"] = q.OverallGrade
			}
		}
		out = append(out, entry)
	}
	return map[string]any{
		"profiles": out,
		"count":    len(out),
		"note":     "각 프로파일은 <data>/profiles/<profile>/ 에 독립 메타데이터 JSON을 가집니다. build_profile_catalog로 라이브 DB에서 구축하고, get_profile_catalog/get_profile_dataset로 조회, put_profile_dataset로 관리하세요.",
	}
}

// getProfileCatalog returns a profile workspace's catalog summary, dataset
// inventory, and health.
func (s *Server) getProfileCatalog(profileID string) map[string]any {
	dir := s.profileCatalogDir(profileID)
	if _, err := os.Stat(dir); err != nil {
		return map[string]any{"profile": profileID, "workspace": false,
			"note": "이 프로파일에는 카탈로그 워크스페이스가 없습니다. build_profile_catalog로 라이브 DB에서 생성하세요."}
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		return map[string]any{"profile": profileID, "workspace": true, "error": "load failed: " + err.Error()}
	}
	q := cat.QualityReport()
	gate := cat.QualityGate()
	blocking := 0
	for _, v := range gate.Violations {
		if v.Severity == "block" {
			blocking++
		}
	}
	return map[string]any{
		"profile":   profileID,
		"workspace": true,
		"dir":       dir,
		"summary":   cat.Summary(),
		"datasets":  cat.DatasetStatus(),
		"health":    cat.Health(),
		"quality": map[string]any{
			"overall_score": q.OverallScore,
			"overall_grade": q.OverallGrade,
			"gate_pass":     gate.Pass,
			"blocking":      blocking,
		},
	}
}

// buildProfileCatalog collects a profile DB's live physical model and writes it
// into the profile's workspace (meta_physical_models.json + relations). ADMIN.
func (s *Server) buildProfileCatalog(ctx context.Context, profileID string, schemas []string, prune bool) map[string]any {
	snap, err := s.metasyncService().Collect(ctx, metasync.CollectRequest{SourceID: profileID, Schemas: schemas})
	if err != nil {
		return map[string]any{"error": "collect failed: " + err.Error()}
	}
	dir := s.profileCatalogDir(profileID)
	if err := ensureWorkspaceScaffold(dir); err != nil {
		return map[string]any{"error": err.Error()}
	}
	cols, rels := snapshotToPhysical(snap)
	// a workspace-scoped catalog: apply merges physical facts into the
	// workspace files (preserving any existing descriptions there).
	pc := &catalog.Catalog{DataDir: dir, Tables: map[string]*catalog.Table{}}
	res := pc.ApplyPhysicalSnapshot(cols, rels, prune, profileID, time.Now())
	res["profile"] = profileID
	res["dir"] = dir
	res["dialect"] = snap.Dialect
	if errMsg, _ := res["error"].(string); errMsg != "" {
		return res
	}
	// report the resulting workspace catalog summary
	if cat, err := catalog.Load(dir); err == nil {
		res["summary"] = cat.Summary()
	}
	res["note"] = "프로파일 워크스페이스에 물리 모델을 구축했습니다. get_profile_catalog로 조회, put_profile_dataset로 논리명·용어집 등 업무 메타데이터를 추가 관리하세요. 이 워크스페이스를 활성 카탈로그로 쓰려면 서버를 -data " + dir + " 로 기동하세요."
	return res
}

// getProfileDataset returns one dataset JSON file's raw content from a
// profile's workspace.
func (s *Server) getProfileDataset(profileID, name string) map[string]any {
	dir := s.profileCatalogDir(profileID)
	info, body, err := catalog.DatasetContent(dir, name)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{
		"profile": profileID, "dataset": info.Name, "file": info.File,
		"content": json.RawMessage(nonEmptyJSON(body)),
	}
}

// putProfileDataset validates and writes one dataset JSON file into a profile's
// workspace, backing up the previous file and rolling back on load failure.
// ADMIN.
func (s *Server) putProfileDataset(profileID, name string, content json.RawMessage) map[string]any {
	dir := s.profileCatalogDir(profileID)
	if err := ensureWorkspaceScaffold(dir); err != nil {
		return map[string]any{"error": err.Error()}
	}
	info, backup, err := catalog.ReplaceDataset(dir, name, content)
	if err != nil {
		return map[string]any{"applied": false, "error": err.Error()}
	}
	// validate by recompiling the workspace; roll back on failure
	if _, lerr := catalog.Load(dir); lerr != nil {
		_ = catalog.RestoreDatasetBackup(dir, info.File, backup)
		return map[string]any{"applied": false, "error": "workspace failed to compile, rolled back: " + lerr.Error(), "backup": backup}
	}
	return map[string]any{"applied": true, "profile": profileID, "dataset": info.Name, "file": info.File, "backup": backup}
}

func nonEmptyJSON(b []byte) string {
	if len(b) == 0 {
		return "null"
	}
	return string(b)
}

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jamypg/internal/catalog"
	"jamypg/internal/openmetadata"
)

// OpenMetadata integration (bidirectional). Import pulls curated business
// metadata (descriptions, display names, PII tags, glossary) from an
// OpenMetadata server and proposes it as jamypg candidates — preview by
// default, explicit apply to merge into overrides/glossary (gaps only,
// operator curation protected). Export pushes jamypg-owned logical names /
// descriptions back to OpenMetadata for columns it lacks (dry-run by default).

// omConfigFile is the persisted, restart-free connection config, stored next to
// the dataset (same posture as db_profiles.json). Runtime config from the admin
// console takes precedence over the -openmetadata-url/-token flags/env.
type omConfigFile struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

func (s *Server) omConfigPath() string {
	return filepath.Join(s.opDir(), "openmetadata.json")
}

// omConfig resolves the effective connection: the stored file wins over the
// flag/env Options. Returns url, token, and where it came from.
func (s *Server) omConfig() (url, token, source string) {
	if b, err := os.ReadFile(s.omConfigPath()); err == nil {
		var cfg omConfigFile
		if json.Unmarshal(b, &cfg) == nil && strings.TrimSpace(cfg.URL) != "" {
			return strings.TrimSpace(cfg.URL), strings.TrimSpace(cfg.Token), "file"
		}
	}
	if u := strings.TrimSpace(s.Options.OpenMetadataURL); u != "" {
		return u, strings.TrimSpace(s.Options.OpenMetadataToken), "flag"
	}
	return "", "", "unset"
}

func (s *Server) omClient() (*openmetadata.Client, error) {
	url, token, _ := s.omConfig()
	if url == "" {
		return nil, errors.New("OpenMetadata is not configured; set it in /admin/openmetadata, or pass -openmetadata-url (and -openmetadata-token) / JAMYPG_OPENMETADATA_URL/_TOKEN")
	}
	return openmetadata.New(url, token), nil
}

// saveOMConfig persists the runtime connection config atomically. An empty URL
// removes the file (reverting to flag/env).
func (s *Server) saveOMConfig(url, token string) error {
	path := s.omConfigPath()
	url = strings.TrimSpace(url)
	if url == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// keep the existing token when the caller submits a blank (masked) token
	if strings.TrimSpace(token) == "" {
		if _, tok, src := s.omConfig(); src == "file" {
			token = tok
		}
	}
	b, err := json.MarshalIndent(omConfigFile{URL: url, Token: strings.TrimSpace(token)}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// omStatus tests connectivity/auth and reports the configured target.
func (s *Server) omStatus(ctx context.Context) map[string]any {
	c, err := s.omClient()
	if err != nil {
		return map[string]any{"configured": false, "error": err.Error()}
	}
	_, token, source := s.omConfig()
	base := map[string]any{"configured": true, "base_url": c.BaseURL, "config_source": source, "has_token": token != ""}
	v, err := c.Version(ctx)
	if err != nil {
		base["reachable"] = false
		base["error"] = err.Error()
		return base
	}
	base["reachable"] = true
	base["server_version"] = v
	return base
}

// omImport fetches OpenMetadata metadata for a scope and proposes it. apply
// merges into the dataset files and reloads the catalog.
func (s *Server) omImport(ctx context.Context, scope string, maxTables int, includeGlossary, apply, toReview bool) map[string]any {
	imp, fetched, err := s.omBuildImport(ctx, scope, maxTables, includeGlossary)
	if err != nil {
		return map[string]any{"error": "list tables failed: " + err.Error()}
	}

	// review mode: stage logical-name/description gaps into the review queue
	// instead of applying, so a human approves them via the normal workflow.
	if toReview {
		res := s.cat().StageExternalImport(imp)
		res["fetched_tables"] = fetched
		res["mode"] = "review"
		return res
	}

	res := s.cat().ImportExternalMetadata(imp, apply, time.Now())
	res["fetched_tables"] = fetched
	if applied, _ := res["applied"].(bool); applied {
		if reload, rerr := s.reloadCatalog(); rerr == nil {
			res["reloaded"] = reload
		} else {
			res["reload_error"] = rerr.Error()
		}
	}
	return res
}

// omBuildImport fetches OpenMetadata tables/glossary and maps them to the
// neutral ExternalImport (shared by import and drift).
func (s *Server) omBuildImport(ctx context.Context, scope string, maxTables int, includeGlossary bool) (catalog.ExternalImport, int, error) {
	c, err := s.omClient()
	if err != nil {
		return catalog.ExternalImport{}, 0, err
	}
	tables, terr := c.ListTables(ctx, scope, maxTables)
	if terr != nil && len(tables) == 0 {
		return catalog.ExternalImport{}, 0, terr
	}
	imp := catalog.ExternalImport{Source: "openmetadata"}
	for _, t := range tables {
		fqn := openmetadata.SchemaTable(t.FullyQualifiedName)
		if fqn == "" {
			continue
		}
		imp.Tables = append(imp.Tables, catalog.ExternalTableMeta{Table: fqn, LogicalName: t.DisplayName, Description: t.Description})
		for _, col := range t.Columns {
			imp.Columns = append(imp.Columns, catalog.ExternalColumnMeta{
				Table: fqn, Column: col.Name, LogicalName: col.DisplayName, Description: col.Description, PII: col.IsPII(),
			})
		}
	}
	if includeGlossary {
		if terms, gerr := c.ListGlossaryTerms(ctx, 500); gerr == nil {
			for _, gt := range terms {
				name := gt.DisplayName
				if name == "" {
					name = gt.Name
				}
				imp.Glossary = append(imp.Glossary, catalog.ExternalGlossaryTerm{Term: name, Synonyms: gt.Synonyms, Description: gt.Description, Category: "imported"})
			}
		}
	}
	return imp, len(tables), nil
}

// omDrift reports where jamypg and OpenMetadata diverge (gaps / conflicts).
func (s *Server) omDrift(ctx context.Context, scope string, maxTables int) map[string]any {
	imp, fetched, err := s.omBuildImport(ctx, scope, maxTables, false)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	res := s.cat().DiffExternalMetadata(imp)
	res["fetched_tables"] = fetched
	return res
}

// omExport pushes jamypg logical names / descriptions to OpenMetadata columns
// that lack a description there. dryRun (default) returns the plan only.
func (s *Server) omExport(ctx context.Context, scope string, maxTables int, dryRun bool) map[string]any {
	c, err := s.omClient()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	tables, terr := c.ListTables(ctx, scope, maxTables)
	if terr != nil && len(tables) == 0 {
		return map[string]any{"error": "list tables failed: " + terr.Error()}
	}
	cat := s.cat()

	type change struct {
		Table  string `json:"table"`
		Column string `json:"column,omitempty"`
		Field  string `json:"field"`
		Value  string `json:"value"`
		Pushed bool   `json:"pushed"`
		Error  string `json:"error,omitempty"`
	}
	var plan []change
	pushed, failed := 0, 0

	for _, t := range tables {
		fqn := openmetadata.SchemaTable(t.FullyQualifiedName)
		jt, ok := cat.ResolveTable(fqn)
		if !ok {
			continue
		}
		for i, col := range t.Columns {
			if col.Description != "" {
				continue // never overwrite OpenMetadata-curated descriptions
			}
			jc := jt.ColumnMap[cleanIdentExport(col.Name)]
			if jc == nil {
				continue
			}
			desc := jamypgColumnDescription(jt, jc)
			if desc == "" {
				continue
			}
			ch := change{Table: fqn, Column: col.Name, Field: "description", Value: desc}
			if !dryRun {
				if perr := c.PatchColumnDescription(ctx, t.ID, i, desc); perr != nil {
					ch.Error = perr.Error()
					failed++
				} else {
					ch.Pushed = true
					pushed++
				}
			}
			plan = append(plan, ch)
		}
	}

	res := map[string]any{
		"source":  "jamypg",
		"target":  c.BaseURL,
		"dry_run": dryRun,
		"planned": len(plan),
		"changes": plan,
		"note":    "OpenMetadata에 이미 설명이 있는 컬럼은 건드리지 않습니다(빈 필드만 채움).",
	}
	if !dryRun {
		res["pushed"] = pushed
		res["failed"] = failed
	}
	if terr != nil {
		res["fetch_warning"] = terr.Error()
	}
	return res
}

// omExportLineage pushes jamypg's relation graph to OpenMetadata as table-level
// lineage edges (fromEntity = referenced/parent table, toEntity = base/child
// table). This maps jamypg's FK-style relationships to OpenMetadata's
// relationship lineage; it is NOT ETL data-flow. dryRun (default) returns the
// plan only.
func (s *Server) omExportLineage(ctx context.Context, scope string, maxTables int, dryRun bool) map[string]any {
	c, err := s.omClient()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	tables, terr := c.ListTables(ctx, scope, maxTables)
	if terr != nil && len(tables) == 0 {
		return map[string]any{"error": "list tables failed: " + terr.Error()}
	}
	// map jamypg-form schema.table (uppercased) → OpenMetadata entity id
	idByFQN := map[string]string{}
	for _, t := range tables {
		if fqn := openmetadata.SchemaTable(t.FullyQualifiedName); fqn != "" && t.ID != "" {
			idByFQN[strings.ToUpper(fqn)] = t.ID
		}
	}

	type edge struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Pushed  bool   `json:"pushed"`
		Skipped string `json:"skipped,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	var plan []edge
	pushed, failed, skipped := 0, 0, 0

	for _, r := range s.cat().Relations {
		fromFQN := r.ReferenceSchema + "." + r.ReferenceTable // parent (upstream)
		toFQN := r.BaseSchema + "." + r.BaseTable             // child (downstream)
		e := edge{From: fromFQN, To: toFQN}
		fromID, okF := idByFQN[strings.ToUpper(fromFQN)]
		toID, okT := idByFQN[strings.ToUpper(toFQN)]
		if !okF || !okT {
			e.Skipped = "not found in OpenMetadata"
			skipped++
			plan = append(plan, e)
			continue
		}
		if fromID == toID {
			e.Skipped = "self-reference"
			skipped++
			plan = append(plan, e)
			continue
		}
		if !dryRun {
			desc := "jamypg relation " + r.BaseColumn + " → " + r.ReferenceColumn
			if perr := c.AddTableLineage(ctx, fromID, toID, desc); perr != nil {
				e.Error = perr.Error()
				failed++
			} else {
				e.Pushed = true
				pushed++
			}
		}
		plan = append(plan, e)
	}

	res := map[string]any{
		"source":    "jamypg",
		"target":    c.BaseURL,
		"dry_run":   dryRun,
		"relations": len(s.cat().Relations),
		"planned":   len(plan) - skipped,
		"skipped":   skipped,
		"edges":     plan,
		"note":      "jamypg 관계(FK)를 OpenMetadata 관계형 lineage로 매핑합니다(ETL 데이터흐름 아님). from=참조(부모), to=기준(자식) 테이블.",
	}
	if !dryRun {
		res["pushed"] = pushed
		res["failed"] = failed
	}
	if terr != nil {
		res["fetch_warning"] = terr.Error()
	}
	return res
}

// jamypgColumnDescription renders a description jamypg can contribute back:
// prefer an explicit description, else compose from logical name.
func jamypgColumnDescription(t *catalog.Table, c *catalog.Column) string {
	if strings.TrimSpace(c.Description) != "" {
		return c.Description
	}
	ln := c.LogicalNameOr()
	if ln == "" || strings.EqualFold(ln, c.Name) {
		return ""
	}
	return t.LogicalNameOr() + "의 " + ln
}

// cleanIdentExport upper-cases a column name to match catalog ColumnMap keys.
func cleanIdentExport(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

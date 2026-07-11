package mcp

import (
	"context"
	"errors"
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

func (s *Server) omClient() (*openmetadata.Client, error) {
	url := strings.TrimSpace(s.Options.OpenMetadataURL)
	if url == "" {
		return nil, errors.New("OpenMetadata is not configured; set -openmetadata-url (and -openmetadata-token) or JAMYPG_OPENMETADATA_URL/_TOKEN")
	}
	return openmetadata.New(url, s.Options.OpenMetadataToken), nil
}

// omStatus tests connectivity/auth and reports the configured target.
func (s *Server) omStatus(ctx context.Context) map[string]any {
	c, err := s.omClient()
	if err != nil {
		return map[string]any{"configured": false, "error": err.Error()}
	}
	v, err := c.Version(ctx)
	if err != nil {
		return map[string]any{"configured": true, "base_url": c.BaseURL, "reachable": false, "error": err.Error()}
	}
	return map[string]any{"configured": true, "base_url": c.BaseURL, "reachable": true, "server_version": v}
}

// omImport fetches OpenMetadata metadata for a scope and proposes it. apply
// merges into the dataset files and reloads the catalog.
func (s *Server) omImport(ctx context.Context, scope string, maxTables int, includeGlossary, apply bool) map[string]any {
	c, err := s.omClient()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	tables, terr := c.ListTables(ctx, scope, maxTables)
	if terr != nil && len(tables) == 0 {
		return map[string]any{"error": "list tables failed: " + terr.Error()}
	}

	imp := catalog.ExternalImport{Source: "openmetadata"}
	for _, t := range tables {
		fqn := openmetadata.SchemaTable(t.FullyQualifiedName)
		if fqn == "" {
			continue
		}
		imp.Tables = append(imp.Tables, catalog.ExternalTableMeta{
			Table: fqn, LogicalName: t.DisplayName, Description: t.Description,
		})
		for _, col := range t.Columns {
			imp.Columns = append(imp.Columns, catalog.ExternalColumnMeta{
				Table:       fqn,
				Column:      col.Name,
				LogicalName: col.DisplayName,
				Description: col.Description,
				PII:         col.IsPII(),
			})
		}
	}

	if includeGlossary {
		terms, gerr := c.ListGlossaryTerms(ctx, 500)
		if gerr == nil {
			for _, gt := range terms {
				name := gt.DisplayName
				if name == "" {
					name = gt.Name
				}
				imp.Glossary = append(imp.Glossary, catalog.ExternalGlossaryTerm{
					Term: name, Synonyms: gt.Synonyms, Description: gt.Description, Category: "imported",
				})
			}
		}
	}

	res := s.cat().ImportExternalMetadata(imp, apply, time.Now())
	res["fetched_tables"] = len(tables)
	if terr != nil {
		res["fetch_warning"] = terr.Error() // partial page failure
	}
	if applied, _ := res["applied"].(bool); applied {
		if reload, rerr := s.reloadCatalog(); rerr == nil {
			res["reloaded"] = reload
		} else {
			res["reload_error"] = rerr.Error()
		}
	}
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

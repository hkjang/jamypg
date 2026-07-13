package mcp

import (
	"context"
	"sync"
	"time"

	"jamypg/internal/catalog"
	"jamypg/internal/metasync"
)

// metasync wiring: the collection service is built lazily from the DB manager
// (which satisfies metasync.SystemQuerier via SystemQuery/ProfileDialect) and
// the active dataset directory. Snapshots persist under
// <dataDir>/metasync/snapshots, so the feature works in standalone mode.
var (
	metaSyncSvc *metasync.Service
	metaSyncMu  sync.Mutex
	metaSyncDir string
)

func (s *Server) metasyncService() *metasync.Service {
	dir := s.cat().DataDir
	metaSyncMu.Lock()
	defer metaSyncMu.Unlock()
	if metaSyncSvc == nil || metaSyncDir != dir {
		metaSyncSvc = metasync.NewService(s.DB, dir)
		metaSyncDir = dir
	}
	return metaSyncSvc
}

// mcpMetadataSources lists the DB profiles usable as metadata collection
// sources — the same permission-filtered set used for query routing.
func (s *Server) mcpMetadataSources(ctx context.Context) map[string]any {
	profs, err := s.usableProfiles(ctx)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := make([]map[string]any, 0, len(profs))
	for _, p := range profs {
		out = append(out, map[string]any{
			"source_id":      p.ID,
			"name":           p.Name,
			"type":           p.Type,
			"connect_target": p.Masked()["connect_string"],
		})
	}
	return map[string]any{
		"sources": out,
		"count":   len(out),
		"note":    "run_metadata_sync / discover_metadata / diff_metadata_snapshots 의 source 인자로 아래 source_id를 사용하세요. 물리 메타데이터는 자동 수집되지만 업무 의미(논리명·지표 등)는 승인 기반으로 관리됩니다.",
	}
}

func (s *Server) mcpDiscoverMetadata(ctx context.Context, sourceID string) map[string]any {
	schemas, err := s.metasyncService().DiscoverSchemas(ctx, sourceID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"source_id": sourceID, "schemas": schemas, "count": len(schemas)}
}

func (s *Server) mcpRunMetadataSync(ctx context.Context, sourceID string, schemas []string, incremental, includeViews bool) map[string]any {
	req := metasync.CollectRequest{
		SourceID: sourceID, Schemas: schemas,
		IncludeViews: includeViews,
	}
	res, err := s.metasyncService().Sync(ctx, req, incremental)
	if err != nil {
		return map[string]any{"status": "sync_failed", "error": err.Error()}
	}
	out := map[string]any{
		"status":         "ok",
		"snapshot":       snapshotSummary(res.Snapshot),
		"skipped":        res.Skipped,
		"baseline":       res.BaselineID,
		"changed_tables": res.ChangeSet.ChangedTables,
		"change_count":   len(res.ChangeSet.Changes),
		"changes":        res.ChangeSet.Changes,
		"change_summary": res.ChangeSet.Summary,
	}
	if res.Note != "" {
		out["note"] = res.Note
	}
	out["principle"] = "물리 구조는 스냅숏으로 자동 수집되었습니다. 삭제는 즉시 반영되지 않고 폐기 후보로 표시되며, 업무 의미 보강은 별도 승인 워크플로에서 처리됩니다."
	return out
}

// mcpApplyMetadataSync reflects a source's latest collected snapshot into the
// catalog dataset files (physical model + FK relations) and reloads. ADMIN
// ONLY — the caller enforces authorization. Physical facts are auto-applied;
// business descriptions are preserved; deletions are retire candidates unless
// prune=true.
func (s *Server) mcpApplyMetadataSync(sourceID string, prune bool) map[string]any {
	snap, err := s.metasyncService().LatestSnapshot(sourceID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if snap == nil {
		return map[string]any{"error": "no snapshot for source " + sourceID + "; run run_metadata_sync first"}
	}

	cols, rels := snapshotToPhysical(snap)
	res := s.cat().ApplyPhysicalSnapshot(cols, rels, prune, sourceID, time.Now())
	res["snapshot_id"] = snap.SnapshotID
	if errMsg, _ := res["error"].(string); errMsg != "" {
		return res
	}
	if reload, rerr := s.reloadCatalog(); rerr == nil {
		res["reloaded"] = reload
	} else {
		res["reload_error"] = rerr.Error()
	}
	return res
}

// snapshotToPhysical flattens a raw snapshot into neutral physical columns +
// FK relations for the catalog apply layer.
func snapshotToPhysical(snap *metasync.RawSnapshot) ([]catalog.PhysicalColumn, []catalog.RelationUpsert) {
	var cols []catalog.PhysicalColumn
	var rels []catalog.RelationUpsert
	for _, t := range snap.Tables {
		for _, col := range t.Columns {
			length := col.FullType
			cols = append(cols, catalog.PhysicalColumn{
				Schema: t.Schema, Table: t.Name, Column: col.Name, Ordinal: col.Ordinal,
				DataType: col.DataType, LengthPrecision: length, Nullable: col.Nullable,
				IsPK: col.IsPrimaryKey, IsFK: col.IsForeignKey, Comment: col.Comment,
			})
		}
		for _, cons := range t.Constraints {
			if cons.Type != "FOREIGN KEY" || len(cons.Columns) == 0 || len(cons.RefColumns) == 0 {
				continue
			}
			// pair base/ref columns positionally
			for i := range cons.Columns {
				refCol := cons.RefColumns[0]
				if i < len(cons.RefColumns) {
					refCol = cons.RefColumns[i]
				}
				rels = append(rels, catalog.RelationUpsert{
					BaseSchema: t.Schema, BaseTable: t.Name, BaseColumn: cons.Columns[i],
					RefSchema: cons.RefSchema, RefTable: cons.RefTable, RefColumn: refCol,
				})
			}
		}
	}
	return cols, rels
}

func (s *Server) mcpSyncStatus(sourceID string) map[string]any {
	list, err := s.metasyncService().Snapshots(sourceID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	summaries := make([]map[string]any, 0, len(list))
	for i := range list {
		summaries = append(summaries, snapshotSummary(&list[i]))
	}
	return map[string]any{"source_id": sourceID, "snapshots": summaries, "count": len(summaries)}
}

func (s *Server) mcpDiffSnapshots(sourceID, fromID, toID string) map[string]any {
	cs, err := s.metasyncService().DiffSnapshots(sourceID, fromID, toID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{
		"source_id":      sourceID,
		"from":           fromID,
		"to":             toID,
		"changed_tables": cs.ChangedTables,
		"change_summary": cs.Summary,
		"changes":        cs.Changes,
	}
}

func (s *Server) mcpProfileMetadata(ctx context.Context, sourceID string, tables []string, mode string, sampleLimit int, piiColumns []string) map[string]any {
	req := metasync.ProfileRequest{
		SourceID: sourceID, Tables: tables, Mode: metasync.ProfileMode(mode),
		SampleLimit: sampleLimit, PIIColumns: piiColumns,
	}
	res, err := s.metasyncService().Profile(ctx, req)
	if err != nil {
		return map[string]any{"status": "profile_failed", "error": err.Error()}
	}
	sensitive := 0
	for _, c := range res.Columns {
		if c.Sensitive {
			sensitive++
		}
	}
	return map[string]any{
		"status":            "ok",
		"profile_id":        res.ProfileID,
		"mode":              res.Mode,
		"sample_limit":      res.SampleLimit,
		"scanned_tables":    res.ScannedTables,
		"column_count":      len(res.Columns),
		"sensitive_columns": sensitive,
		"columns":           res.Columns,
		"warnings":          res.Warnings,
		"note":              res.Note,
	}
}

func (s *Server) mcpProfileStatus(sourceID string) map[string]any {
	list, err := s.metasyncService().Profiles(sourceID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := make([]map[string]any, 0, len(list))
	for _, r := range list {
		out = append(out, map[string]any{
			"profile_id": r.ProfileID, "mode": r.Mode, "profiled_at": r.ProfiledAt,
			"scanned_tables": r.ScannedTables, "sample_limit": r.SampleLimit,
		})
	}
	return map[string]any{"source_id": sourceID, "profiles": out, "count": len(out)}
}

func snapshotSummary(s *metasync.RawSnapshot) map[string]any {
	return map[string]any{
		"snapshot_id":  s.SnapshotID,
		"source_id":    s.SourceID,
		"dialect":      s.Dialect,
		"collected_at": s.CollectedAt,
		"schema_hash":  s.SchemaHash,
		"object_count": s.ObjectCount,
		"status":       s.Status,
	}
}

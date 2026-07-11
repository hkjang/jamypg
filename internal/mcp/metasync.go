package mcp

import (
	"context"
	"sync"

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

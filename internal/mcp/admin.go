package mcp

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"jamypg/internal/catalog"
)

//go:embed webui
var webuiFS embed.FS

// registerAdmin wires the REST management API (consumed by /admin and any
// HTTP client), the Swagger UI at /docs, and the admin console at /admin.
// The REST layer reuses the exact same dataset operations as the MCP tools
// (put_dataset / remove_dataset / reload_catalog), so behavior — validation,
// backup, hot-swap, rollback — is identical on both surfaces.
func (s *Server) registerAdmin(mux *http.ServeMux) {
	// static UI
	mux.HandleFunc("GET /{$}", s.serveWebUI("webui/landing.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /admin/nav.js", s.serveWebUI("webui/nav.js", "application/javascript"))
	mux.HandleFunc("GET /admin/ask", s.guardPage(s.serveWebUI("webui/ask.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/history", s.guardPage(s.serveWebUI("webui/history.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/stats", s.guardPage(s.serveWebUI("webui/stats.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin", s.guardPage(s.serveWebUI("webui/admin.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/editor", s.guardPage(s.serveWebUI("webui/editor.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/db", s.guardPage(s.serveWebUI("webui/db.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/reviews", s.guardPage(s.serveWebUI("webui/reviews.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/quality", s.guardPage(s.serveWebUI("webui/quality.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/users", s.guardAdminPage(s.serveWebUI("webui/users.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/keys", s.guardPage(s.serveWebUI("webui/keys.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /admin/settings", s.guardAdminPage(s.serveWebUI("webui/settings.html", "text/html; charset=utf-8")))
	mux.HandleFunc("GET /docs", s.serveWebUI("webui/docs.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /docs/swagger-ui.css", s.serveWebUI("webui/swagger-ui.css", "text/css"))
	mux.HandleFunc("GET /docs/swagger-ui-bundle.js", s.serveWebUI("webui/swagger-ui-bundle.js", "application/javascript"))
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openAPISpec))
	})

	// read-only API
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.cat().Health())
	})
	mux.HandleFunc("GET /api/metadata/quality", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("gate") == "true" {
			writeJSON(w, http.StatusOK, s.cat().QualityGate())
			return
		}
		writeJSON(w, http.StatusOK, s.cat().QualityReport())
	})
	mux.HandleFunc("POST /api/metadata/suggest", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tables []string `json:"tables"`
			Kinds  []string `json:"kinds"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		}
		writeJSON(w, http.StatusOK, s.cat().SuggestSemanticMetadata(req.Tables, req.Kinds))
	})
	mux.HandleFunc("POST /api/metadata/candidates", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tables []string `json:"tables"`
			Kinds  []string `json:"kinds"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		}
		writeJSON(w, http.StatusOK, s.cat().SuggestModelCandidates(req.Tables, req.Kinds))
	})
	mux.HandleFunc("GET /api/metadata/digest", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.MetadataDigest())
	})
	mux.HandleFunc("GET /api/audit/verify", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, s.VerifyAuditChain(r.URL.Query().Get("day")))
	})
	mux.HandleFunc("GET /api/metadata/impact", func(w http.ResponseWriter, r *http.Request) {
		table := r.URL.Query().Get("table")
		column := r.URL.Query().Get("column")
		if table == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "table query parameter is required"})
			return
		}
		writeJSON(w, http.StatusOK, s.cat().AnalyzeImpact(table, column))
	})
	mux.HandleFunc("GET /api/reviews", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var tables, kinds []string
		if v := q.Get("tables"); v != "" {
			tables = splitCSV(v)
		}
		if v := q.Get("kinds"); v != "" {
			kinds = splitCSV(v)
		}
		writeJSON(w, http.StatusOK, s.cat().ReviewCandidates(tables, kinds, q.Get("status")))
	})
	mux.HandleFunc("GET /api/reviews/apply", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.cat().ApprovedOverrides())
	})
	mux.HandleFunc("POST /api/reviews/apply", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		res, _ := s.applyApprovedCandidates()
		s.adminAudit(r, "reviews.apply", s.reviewerFromRequest(r), nil)
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("GET /api/golden/candidates", func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		writeJSON(w, http.StatusOK, s.cat().SuggestGoldenFromFeedback(limit))
	})
	mux.HandleFunc("POST /api/golden/promote", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		var req struct {
			FeedbackIDs []string `json:"feedback_ids"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		}
		res := s.cat().PromoteGolden(req.FeedbackIDs, time.Now())
		if applied, _ := res["applied"].(int); applied > 0 {
			if reload, err := s.reloadCatalog(); err == nil {
				res["reloaded"] = reload
			} else {
				res["reload_error"] = err.Error()
			}
		}
		s.adminAudit(r, "golden.promote", s.reviewerFromRequest(r), nil)
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /api/reviews/decide", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		var req struct {
			Decisions []catalog.DecideCandidate `json:"decisions"`
			Reviewer  string                    `json:"reviewer"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		}
		reviewer := req.Reviewer
		if reviewer == "" {
			reviewer = s.reviewerFromRequest(r)
		}
		res := s.cat().DecideCandidates(req.Decisions, reviewer, time.Now())
		s.adminAudit(r, "reviews.decide", reviewer, nil)
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("GET /api/datasets", func(w http.ResponseWriter, _ *http.Request) {
		storage := "file"
		if s.datasetsInDB() {
			storage = "postgres"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data_dir": s.cat().DataDir,
			"datasets": s.cat().DatasetStatus(),
			"storage":  storage, // file | postgres (meta DB is source of truth)
		})
	})
	mux.HandleFunc("GET /api/datasets/{name}", func(w http.ResponseWriter, r *http.Request) {
		rows := 5
		if v := r.URL.Query().Get("sample_rows"); v != "" {
			rows = atoiDefault(v, 5)
		}
		res, err := s.cat().DatasetSample(r.PathValue("name"), rows)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("GET /api/datasets/{name}/content", func(w http.ResponseWriter, r *http.Request) {
		d, b, err := catalog.DatasetContent(s.cat().DataDir, r.PathValue("name"))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Dataset-File", d.File)
		if b == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /api/datasets/{name}/backups", func(w http.ResponseWriter, r *http.Request) {
		d, backups, err := catalog.ListDatasetBackups(s.cat().DataDir, r.PathValue("name"))
		if err != nil {
			writeAPIError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"dataset": d.Name, "file": d.File, "backups": backups})
	})

	// mutating API (admin token enforced when configured)
	mux.HandleFunc("PUT /api/datasets/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		force := r.URL.Query().Get("force") == "true"
		name := r.PathValue("name")
		res, err := s.putDataset(name, body, force)
		s.adminAudit(r, "put_dataset", name, err)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("DELETE /api/datasets/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		name := r.PathValue("name")
		res, err := s.removeDataset(name)
		s.adminAudit(r, "remove_dataset", name, err)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /api/datasets/{name}/restore", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		var req struct {
			Backup string `json:"backup"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		name := r.PathValue("name")
		res, err := s.restoreDataset(name, req.Backup)
		s.adminAudit(r, "restore_dataset", name+" <- "+req.Backup, err)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /api/reload", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		res, err := s.reloadCatalog()
		s.adminAudit(r, "reload_catalog", "", err)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// restoreDataset copies a backup over the live file (backing up the current
// file first, so a restore is itself reversible), then hot-swaps the catalog.
func (s *Server) restoreDataset(name, backupName string) (map[string]any, error) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	dataDir := s.cat().DataDir
	d, backupPath, err := catalog.ResolveBackupPath(dataDir, name, backupName)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(backupPath)
	if err != nil {
		return nil, err
	}
	return s.applyDatasetBytes(d, dataDir, b, "restored from "+backupName)
}

func (s *Server) applyDatasetBytes(d catalog.DatasetInfo, dataDir string, content []byte, action string) (map[string]any, error) {
	res, err := s.putDatasetLocked(d.Name, content, true)
	if err != nil {
		return nil, err
	}
	res["action"] = action
	return res, nil
}

// reviewerFromRequest resolves who is recording a candidate decision: the
// authenticated user when auth is on, else the X-Reviewer header, else "admin".
func (s *Server) reviewerFromRequest(r *http.Request) string {
	if s.authEnabled() {
		if u, err := s.authenticate(r); err == nil && u != nil && u.Username != "" {
			return u.Username
		}
	}
	if h := strings.TrimSpace(r.Header.Get("X-Reviewer")); h != "" {
		return h
	}
	return "admin"
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	// meta DB active: admin ROLE required (master token counts as admin)
	if s.authEnabled() {
		u, err := s.authenticate(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "authentication required",
				"hint":  "로그인 세션, MCP 키, 또는 X-Admin-Token이 필요합니다.",
			})
			return false
		}
		if !u.IsAdmin() {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "admin role required",
				"hint":  "이 작업은 관리자 전용입니다. 관리자에게 역할 승격을 요청하세요.",
			})
			return false
		}
		return true
	}
	// standalone: legacy master-token gate
	token := strings.TrimSpace(s.Options.AdminToken)
	if token == "" {
		return true // auth disabled (internal network); enable with -admin-token
	}
	got := r.Header.Get("X-Admin-Token")
	if got == "" {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "admin token required",
			"hint":  "X-Admin-Token 헤더 또는 Authorization: Bearer <token>을 보내세요. 토큰은 서버 -admin-token 플래그로 설정됩니다.",
		})
		return false
	}
	return true
}

// adminAudit records REST mutations into the same audit JSONL as MCP calls.
func (s *Server) adminAudit(r *http.Request, action, detail string, callErr error) {
	entry := map[string]any{
		"ts":     time.Now().Format(time.RFC3339Nano),
		"tool":   "admin:" + action,
		"detail": detail,
		"remote": r.RemoteAddr,
	}
	if callErr != nil {
		entry["is_error"] = true
		entry["error"] = callErr.Error()
	}
	s.appendAudit(entry)
}

func (s *Server) serveWebUI(path, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		b, err := webuiFS.ReadFile(path)
		if err != nil {
			http.Error(w, "asset not found: "+path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func atoiDefault(s string, def int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

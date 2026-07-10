package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GoldenQuery is one entry of the CI-runnable evaluation set
// (data/<set>/golden_queries.json).
type GoldenQuery struct {
	ID              any      `json:"id,omitempty"`
	Question        string   `json:"question"`
	ExpectedTables  []string `json:"expected_tables"`
	ExpectedColumns []string `json:"expected_columns,omitempty"` // "TABLE.COLUMN" or bare column
	ExpectedMetrics []string `json:"expected_metrics,omitempty"`
	ExpectedSQL     string   `json:"expected_sql,omitempty"`
	// row-count sanity bounds for execution-based evaluation (optional; both
	// unset = only "executes without error" is checked)
	ExpectedMinRows *int64 `json:"expected_min_rows,omitempty"`
	ExpectedMaxRows *int64 `json:"expected_max_rows,omitempty"`
	Note            string `json:"note,omitempty"`
}

// RowCounter executes SELECT COUNT(*) over a golden SQL against a real
// database. Injected (e.g. dbconn.Manager.CountRows) so the catalog package
// stays DB-free; nil disables execution-based evaluation.
type RowCounter func(ctx context.Context, sql string) (int64, error)

type EvalCaseResult struct {
	Question       string   `json:"question"`
	TableHit       bool     `json:"table_hit"`
	TableRank      int      `json:"table_rank,omitempty"` // 1-based rank of first expected table
	ColumnRecall   float64  `json:"column_recall"`
	MetricHit      bool     `json:"metric_hit"`
	JoinPathOK     bool     `json:"join_path_ok"`
	ExpectedSQLOK  *bool    `json:"expected_sql_valid,omitempty"`
	ExecutedRows   *int64   `json:"executed_rows,omitempty"`
	RowSanityOK    *bool    `json:"row_sanity_ok,omitempty"`
	ExecError      string   `json:"exec_error,omitempty"`
	Missing        []string `json:"missing,omitempty"`
	DurationMillis int64    `json:"duration_ms"`
}

// RunEvaluation scores search/metric/join accuracy against the golden set.
// It is deterministic and DB-free, so it runs in CI via `go test ./...` or
// through the run_evaluation MCP tool.
func (c *Catalog) RunEvaluation(goldenPath string, topK int) (map[string]any, error) {
	return c.RunEvaluationExec(context.Background(), goldenPath, topK, nil)
}

// RunEvaluationExec additionally executes each expected_sql through the
// injected RowCounter for execution success rate and row-count sanity
// (expected_min_rows / expected_max_rows bounds).
func (c *Catalog) RunEvaluationExec(ctx context.Context, goldenPath string, topK int, counter RowCounter) (map[string]any, error) {
	if goldenPath == "" {
		goldenPath = filepath.Join(c.DataDir, "golden_queries.json")
	}
	if topK <= 0 {
		topK = 5
	}
	var golden []GoldenQuery
	if err := readJSON(goldenPath, &golden); err != nil {
		return nil, err
	}
	results := make([]EvalCaseResult, 0, len(golden))
	tableHits, metricHits, joinOK, sqlValid, sqlTotal := 0, 0, 0, 0, 0
	execTotal, execSuccess, sanityTotal, sanityOK := 0, 0, 0, 0
	colRecallSum := 0.0
	var totalDur time.Duration
	for _, g := range golden {
		start := time.Now()
		r := EvalCaseResult{Question: g.Question, JoinPathOK: true, MetricHit: true}
		search := c.SearchSchema(SearchRequest{Question: g.Question, TopK: topK, IncludeColumns: true, MaxColumns: 12})
		rankOf := func(want string) int {
			t, ok := c.ResolveTable(want)
			if !ok {
				return 0
			}
			for i, res := range search.Results {
				if res.Table == t.FQN {
					return i + 1
				}
			}
			return 0
		}
		for _, want := range g.ExpectedTables {
			if rank := rankOf(want); rank > 0 {
				r.TableHit = true
				if r.TableRank == 0 || rank < r.TableRank {
					r.TableRank = rank
				}
			} else {
				r.Missing = append(r.Missing, "table:"+want)
			}
		}
		// column recall over matched columns of the expected tables
		if len(g.ExpectedColumns) > 0 {
			found := 0
			for _, want := range g.ExpectedColumns {
				wantCol := cleanIdent(want)
				if i := strings.LastIndex(wantCol, "."); i >= 0 {
					wantCol = wantCol[i+1:]
				}
				hit := false
				for _, res := range search.Results {
					for _, m := range res.MatchedColumns {
						if m.Name == wantCol {
							hit = true
							break
						}
					}
				}
				if hit {
					found++
				} else {
					r.Missing = append(r.Missing, "column:"+want)
				}
			}
			r.ColumnRecall = round(float64(found) / float64(len(g.ExpectedColumns)))
		} else {
			r.ColumnRecall = 1
		}
		for _, want := range g.ExpectedMetrics {
			if len(c.LookupMetrics(want)) == 0 {
				r.MetricHit = false
				r.Missing = append(r.Missing, "metric:"+want)
			}
		}
		if len(g.ExpectedTables) > 1 {
			out, err := c.GetJoinPaths(JoinPathRequest{Tables: g.ExpectedTables})
			if err != nil {
				r.JoinPathOK = false
			} else {
				for _, p := range out["join_paths"].([]JoinPathResult) {
					if !p.Found {
						r.JoinPathOK = false
						r.Missing = append(r.Missing, "join:"+p.From+"->"+p.To)
					}
				}
			}
		}
		if strings.TrimSpace(g.ExpectedSQL) != "" {
			sqlTotal++
			v := c.ValidateSQL(ValidateRequest{SQL: g.ExpectedSQL})
			ok := v.Valid
			r.ExpectedSQLOK = &ok
			if ok {
				sqlValid++
			} else {
				for _, e := range v.Errors {
					r.Missing = append(r.Missing, "sql_error:"+e.Message)
				}
			}
			// execution-based check: run against the real DB when a counter
			// is injected and static validation passed
			if counter != nil && ok {
				execTotal++
				n, err := counter(ctx, g.ExpectedSQL)
				if err != nil {
					r.ExecError = err.Error()
					r.Missing = append(r.Missing, "exec:"+truncateEvalStr(err.Error(), 120))
				} else {
					execSuccess++
					r.ExecutedRows = &n
					if g.ExpectedMinRows != nil || g.ExpectedMaxRows != nil {
						sanityTotal++
						sane := (g.ExpectedMinRows == nil || n >= *g.ExpectedMinRows) &&
							(g.ExpectedMaxRows == nil || n <= *g.ExpectedMaxRows)
						r.RowSanityOK = &sane
						if sane {
							sanityOK++
						} else {
							r.Missing = append(r.Missing, fmt.Sprintf("rows:%d (expected %s..%s)", n, i64str(g.ExpectedMinRows), i64str(g.ExpectedMaxRows)))
						}
					}
				}
			}
		}
		dur := time.Since(start)
		r.DurationMillis = dur.Milliseconds()
		totalDur += dur
		if r.TableHit {
			tableHits++
		}
		if r.MetricHit {
			metricHits++
		}
		if r.JoinPathOK {
			joinOK++
		}
		colRecallSum += r.ColumnRecall
		results = append(results, r)
	}
	n := len(golden)
	summary := map[string]any{
		"golden_path":         goldenPath,
		"cases":               n,
		"table_selection_acc": ratio(tableHits, n),
		"column_recall_avg":   round(colRecallSum / float64(max(1, n))),
		"metric_lookup_acc":   ratio(metricHits, n),
		"join_path_acc":       ratio(joinOK, n),
		"expected_sql_valid":  ratio(sqlValid, max(1, sqlTotal)),
		"avg_response_ms":     totalDur.Milliseconds() / int64(max(1, n)),
		"results":             results,
	}
	if counter != nil {
		summary["execution_checked"] = execTotal
		summary["execution_success_rate"] = ratio(execSuccess, max(1, execTotal))
		summary["row_sanity_checked"] = sanityTotal
		summary["row_sanity_rate"] = ratio(sanityOK, max(1, sanityTotal))
	}
	return summary, nil
}

func i64str(v *int64) string {
	if v == nil {
		return "∞"
	}
	return fmt.Sprint(*v)
}

func truncateEvalStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return round(float64(a) / float64(b))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// FileExists is a small helper for callers deciding whether an optional
// golden set is present.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

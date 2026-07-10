package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFeedbackFixture writes synthetic feedback JSONL into a temp data dir
// and points the already-loaded catalog at it.
func buildFeedbackFixture(t *testing.T, c *Catalog, records []FeedbackRecord) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "feedback"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "feedback", "feedback-test.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	c.DataDir = dir
	return dir
}

func TestLearnFromFeedbackPromotesRules(t *testing.T) {
	c := loadTestCatalog(t)
	origDataDir := c.DataDir
	defer func() { c.DataDir = origDataDir; c.applyLearnedRules(nil) }()

	genSQL := "SELECT T1.USER_ID FROM PUBLIC.JAMYPG_MCP_KEYS T1"
	finSQL := "SELECT T1.USER_ID FROM PUBLIC.JAMYPG_MCP_ACTIVITY T1"
	valErr := []ValidationIssue{{Level: "error", Code: "UNKNOWN_COLUMN", Table: "PUBLIC.JAMYPG_MCP_ACTIVITY", Column: "ELAPSED_SEC"}}
	var records []FeedbackRecord
	for i := 0; i < 3; i++ {
		records = append(records,
			FeedbackRecord{Question: "사용자별 도구 호출 수", Outcome: "corrected", GeneratedSQL: genSQL, FinalSQL: finSQL},
			FeedbackRecord{Question: "도구별 평균 실행시간", Outcome: "failure", Errors: valErr},
		)
	}
	buildFeedbackFixture(t, c, records)

	res, err := c.LearnFromFeedback(3)
	if err != nil {
		t.Fatalf("LearnFromFeedback() error = %v", err)
	}
	rules := res["rules"].([]LearnedRule)
	var haveSwap, haveErr bool
	for _, r := range rules {
		if r.Type == "table_correction" && r.Table == "PUBLIC.JAMYPG_MCP_KEYS" && r.ReplacementTable == "PUBLIC.JAMYPG_MCP_ACTIVITY" {
			haveSwap = true
		}
		if r.Type == "recurring_error" && r.Code == "UNKNOWN_COLUMN" && r.Column == "ELAPSED_SEC" {
			haveErr = true
		}
	}
	if !haveSwap {
		t.Fatalf("expected table_correction rule, got %+v", rules)
	}
	if !haveErr {
		t.Fatalf("expected recurring_error rule, got %+v", rules)
	}
	if !FileExists(filepath.Join(c.DataDir, learnedRulesFile)) {
		t.Fatal("expected learned_rules.json to be persisted")
	}

	// search penalty applied to the mis-selected table
	if c.FeedbackPenalty["PUBLIC.JAMYPG_MCP_KEYS"] == 0 {
		t.Fatal("expected search penalty for corrected-away table")
	}

	// validate_sql surfaces the learned warning when the bad table is used again
	v := c.ValidateSQL(ValidateRequest{SQL: genSQL + " LIMIT 10"})
	found := false
	for _, w := range v.Warnings {
		if w.Code == "LEARNED_TABLE_CORRECTION" && strings.Contains(w.Hint, "PUBLIC.JAMYPG_MCP_ACTIVITY") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected LEARNED_TABLE_CORRECTION warning, got %+v", v.Warnings)
	}
}

func TestLearnFromFeedbackBelowThreshold(t *testing.T) {
	c := loadTestCatalog(t)
	origDataDir := c.DataDir
	defer func() { c.DataDir = origDataDir; c.applyLearnedRules(nil) }()
	buildFeedbackFixture(t, c, []FeedbackRecord{
		{Question: "q", Outcome: "corrected",
			GeneratedSQL: "SELECT T1.USER_ID FROM PUBLIC.JAMYPG_MCP_KEYS T1",
			FinalSQL:     "SELECT T1.USER_ID FROM PUBLIC.JAMYPG_MCP_ACTIVITY T1"},
	})
	res, err := c.LearnFromFeedback(3)
	if err != nil {
		t.Fatal(err)
	}
	if n := res["promoted"].(int); n != 0 {
		t.Fatalf("one occurrence must not be promoted, got %d rules", n)
	}
}

func TestLearnFromQueryAudit(t *testing.T) {
	c := loadTestCatalog(t)
	origDataDir := c.DataDir
	defer func() { c.DataDir = origDataDir; c.applyLearnedRules(nil) }()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "audit"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "audit", "query-20260704.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	slowSQL := "SELECT T1.ID FROM PUBLIC.JAMYPG_MCP_ACTIVITY T1 WHERE T1.STATUS = 'ok'"
	failSQL := "SELECT T1.USER_ID FROM PUBLIC.JAMYPG_SESSIONS T1"
	enc := json.NewEncoder(f)
	for i := 0; i < 3; i++ {
		_ = enc.Encode(map[string]any{"tool": "db:execute", "entry": map[string]any{
			"sql_text": slowSQL, "elapsed_ms": 6500, "success": true}})
		_ = enc.Encode(map[string]any{"tool": "db:execute", "entry": map[string]any{
			"sql_text": failSQL, "elapsed_ms": 100, "success": false, "error_code": "TIMEOUT"}})
		// CANCELED는 학습 대상이 아님
		_ = enc.Encode(map[string]any{"tool": "db:execute", "entry": map[string]any{
			"sql_text": failSQL, "elapsed_ms": 50, "success": false, "error_code": "CANCELED"}})
	}
	f.Close()
	c.DataDir = dir

	res, err := c.LearnFromFeedback(3)
	if err != nil {
		t.Fatal(err)
	}
	rules := res["rules"].([]LearnedRule)
	var haveSlow, haveExecErr bool
	for _, r := range rules {
		if r.Type == "slow_query" && r.Table == "PUBLIC.JAMYPG_MCP_ACTIVITY" && r.Occurrences == 3 {
			haveSlow = true
		}
		if r.Type == "recurring_exec_error" && r.Table == "PUBLIC.JAMYPG_SESSIONS" && r.Code == "TIMEOUT" {
			haveExecErr = true
		}
		if r.Type == "recurring_exec_error" && r.Code == "CANCELED" {
			t.Fatalf("CANCELED must not be promoted: %+v", r)
		}
	}
	if !haveSlow || !haveExecErr {
		t.Fatalf("expected slow_query and recurring_exec_error rules, got %+v", rules)
	}

	// validate_sql이 해당 테이블 사용 시 학습 경고를 부착
	v := c.ValidateSQL(ValidateRequest{SQL: slowSQL + " LIMIT 10"})
	foundSlow := false
	for _, w := range v.Warnings {
		if w.Code == "LEARNED_SLOW_QUERY" && strings.Contains(w.Hint, "explain_sql") {
			foundSlow = true
		}
	}
	if !foundSlow {
		t.Fatalf("expected LEARNED_SLOW_QUERY warning, got %+v", v.Warnings)
	}
	v = c.ValidateSQL(ValidateRequest{SQL: "SELECT T1.USER_ID FROM PUBLIC.JAMYPG_SESSIONS T1 WHERE T1.REVOKED_AT IS NULL LIMIT 5"})
	foundExec := false
	for _, w := range v.Warnings {
		if w.Code == "LEARNED_EXEC_ERROR" && strings.Contains(w.Message, "TIMEOUT") {
			foundExec = true
		}
	}
	if !foundExec {
		t.Fatalf("expected LEARNED_EXEC_ERROR warning, got %+v", v.Warnings)
	}
}

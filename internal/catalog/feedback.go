package catalog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FeedbackRecord is the persisted shape of record_feedback. Successful,
// adopted SQL feeds few-shot reuse and search-score boosting; failures are
// kept for validation-rule tuning.
type FeedbackRecord struct {
	ID           string   `json:"id,omitempty"`
	RecordedAt   string   `json:"recorded_at,omitempty"`
	Question     string   `json:"question"`
	Analysis     any      `json:"analysis,omitempty"`
	Tables       []string `json:"tables,omitempty"`
	Columns      []string `json:"columns,omitempty"`
	GeneratedSQL string   `json:"generated_sql,omitempty"`
	Errors       any      `json:"validation_errors,omitempty"`
	FinalSQL     string   `json:"final_sql,omitempty"`
	Executed     *bool    `json:"executed,omitempty"`
	Adopted      *bool    `json:"adopted,omitempty"`
	Outcome      string   `json:"outcome"` // success | failure | corrected | rejected
	DurationMS   float64  `json:"duration_ms,omitempty"`
	ResultRows   *int64   `json:"result_rows,omitempty"`
	FailureCause string   `json:"failure_cause,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

var feedbackTableRE = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([A-Za-z_][\w$#]*\s*\.\s*[A-Za-z_][\w$#]*)`)

// loadFeedback aggregates per-table success counts from feedback/*.jsonl so
// search_schema can boost tables that historically produced adopted SQL.
func (c *Catalog) loadFeedback(dataDir string) {
	c.FeedbackUsage = map[string]int{}
	dir := filepath.Join(dataDir, "feedback")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			var rec FeedbackRecord
			if json.Unmarshal(sc.Bytes(), &rec) != nil {
				continue
			}
			outcome := strings.ToLower(rec.Outcome)
			if outcome != "success" && outcome != "corrected" {
				continue
			}
			tables := rec.Tables
			if len(tables) == 0 {
				sql := nonEmpty(rec.FinalSQL, rec.GeneratedSQL)
				for _, m := range feedbackTableRE.FindAllStringSubmatch(sql, -1) {
					tables = append(tables, strings.ReplaceAll(m[1], " ", ""))
				}
			}
			seen := map[string]bool{}
			for _, tn := range tables {
				if t, ok := c.ResolveTable(tn); ok && !seen[t.FQN] {
					seen[t.FQN] = true
					c.FeedbackUsage[t.FQN]++
				}
			}
		}
		f.Close()
	}
}

// SuccessfulFeedbackExamples returns adopted question→SQL pairs matching the
// question tokens, for few-shot reuse alongside sql_datasets examples.
func (c *Catalog) SuccessfulFeedbackExamples(question string, topK int) []map[string]any {
	if topK <= 0 {
		topK = 3
	}
	dir := filepath.Join(c.DataDir, "feedback")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	tokens := c.expandTokens(tokenize(question))
	type scored struct {
		rec   FeedbackRecord
		score float64
	}
	var hits []scored
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			var rec FeedbackRecord
			if json.Unmarshal(sc.Bytes(), &rec) != nil {
				continue
			}
			outcome := strings.ToLower(rec.Outcome)
			if (outcome != "success" && outcome != "corrected") || nonEmpty(rec.FinalSQL, rec.GeneratedSQL) == "" {
				continue
			}
			text := strings.ToLower(rec.Question)
			score := 0.0
			for _, tok := range tokens {
				if strings.Contains(text, strings.ToLower(tok)) {
					score++
				}
			}
			if score > 0 {
				hits = append(hits, scored{rec, score})
			}
		}
		f.Close()
	}
	if len(hits) == 0 {
		return nil
	}
	// simple selection sort of top K to avoid importing sort here twice
	out := []map[string]any{}
	for len(out) < topK && len(hits) > 0 {
		best := 0
		for i := range hits {
			if hits[i].score > hits[best].score {
				best = i
			}
		}
		r := hits[best].rec
		out = append(out, map[string]any{
			"question": r.Question,
			"sql":      nonEmpty(r.FinalSQL, r.GeneratedSQL),
			"outcome":  r.Outcome,
			"source":   "feedback",
			"score":    hits[best].score,
		})
		hits = append(hits[:best], hits[best+1:]...)
	}
	return out
}

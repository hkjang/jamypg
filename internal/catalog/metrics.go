package catalog

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MetricDef is a curated business-metric definition. SQL generation must use
// these expressions instead of letting the LLM invent formulas.
type MetricDef struct {
	Name               string   `json:"name"`
	BusinessName       string   `json:"business_name,omitempty"`
	Aliases            []string `json:"aliases,omitempty"`
	Description        string   `json:"description,omitempty"`
	Expression         string   `json:"expression"`
	Aggregation        string   `json:"aggregation,omitempty"`
	Tables             []string `json:"tables,omitempty"`
	Columns            []string `json:"columns,omitempty"`
	AllowedGrains      []string `json:"allowed_grains,omitempty"`
	RecommendedGroupBy []string `json:"recommended_group_by,omitempty"`
	RequiredFilters    []string `json:"required_filters,omitempty"`
	Exclusions         []string `json:"exclusions,omitempty"`
	NullHandling       string   `json:"null_handling,omitempty"`
	DedupKey           string   `json:"dedup_key,omitempty"`
	ExampleSQL         string   `json:"example_sql,omitempty"`
}

func loadMetrics(dataDir string) ([]MetricDef, []LoadIssue) {
	path := filepath.Join(dataDir, "metrics.json")
	if _, err := os.Stat(path); err != nil {
		return nil, []LoadIssue{{Level: "warning", Source: "metrics.json", Message: "metrics.json not found; metric lookups fall back to naming-convention inference"}}
	}
	var defs []MetricDef
	if err := readJSON(path, &defs); err != nil {
		return nil, []LoadIssue{{Level: "error", Source: "metrics.json", Message: err.Error()}}
	}
	var issues []LoadIssue
	for _, d := range defs {
		if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Expression) == "" {
			issues = append(issues, LoadIssue{Level: "error", Source: "metrics.json", Message: "metric requires name and expression: " + d.Name})
		}
	}
	return defs, issues
}

// validateMetrics cross-checks metric table/column references against the
// compiled catalog so broken definitions surface at startup.
func (c *Catalog) validateMetrics() {
	for _, m := range c.Metrics {
		for _, tn := range m.Tables {
			t, ok := c.ResolveTable(tn)
			if !ok {
				c.Issues = append(c.Issues, LoadIssue{Level: "error", Source: "metrics.json", Message: "metric '" + m.Name + "' references unknown table", Table: tn})
				continue
			}
			for _, col := range m.Columns {
				cn := cleanIdent(col)
				if strings.Contains(cn, ".") {
					continue // qualified elsewhere
				}
				if t.ColumnMap[cn] == nil {
					c.Issues = append(c.Issues, LoadIssue{Level: "warning", Source: "metrics.json", Message: "metric '" + m.Name + "' column not found in " + t.FQN, Column: cn})
				}
			}
		}
	}
}

// LookupMetrics finds dictionary metrics whose name or aliases match the term.
func (c *Catalog) LookupMetrics(term string) []MetricDef {
	lt := strings.ToLower(strings.TrimSpace(term))
	if lt == "" {
		return nil
	}
	type scored struct {
		def   MetricDef
		score int
	}
	var hits []scored
	candidates := []string{lt}
	if c.Glossary != nil {
		expanded, _ := c.Glossary.Expand([]string{lt})
		candidates = expanded
	}
	for _, m := range c.Metrics {
		names := append([]string{m.Name, m.BusinessName}, m.Aliases...)
		best := 0
		for _, n := range names {
			ln := strings.ToLower(strings.TrimSpace(n))
			if ln == "" {
				continue
			}
			for _, cand := range candidates {
				switch {
				case ln == cand:
					if best < 3 {
						best = 3
					}
				case strings.Contains(cand, ln) || strings.Contains(ln, cand):
					if best < 2 {
						best = 2
					}
				}
			}
		}
		if best > 0 {
			hits = append(hits, scored{m, best})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]MetricDef, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.def)
	}
	return out
}

// MetricNamesInQuestion returns dictionary metric names whose name or alias
// literally appears in the question.
func (c *Catalog) MetricNamesInQuestion(question string) []string {
	lq := strings.ToLower(question)
	var out []string
	for _, m := range c.Metrics {
		for _, n := range append([]string{m.Name, m.BusinessName}, m.Aliases...) {
			ln := strings.ToLower(strings.TrimSpace(n))
			if ln != "" && strings.Contains(lq, ln) {
				out = appendUnique(out, m.Name)
				break
			}
		}
	}
	return out
}

// MetricDefinition returns dictionary definitions first; only when the
// dictionary has no entry does it fall back to naming-convention inference,
// and the two are clearly separated so the caller never confuses curated
// formulas with guesses.
func (c *Catalog) MetricDefinition(metricName string, topK int) map[string]any {
	if topK <= 0 {
		topK = 8
	}
	dict := c.LookupMetrics(metricName)
	res := map[string]any{
		"metric_name": metricName,
	}
	if len(dict) > 0 {
		if len(dict) > topK {
			dict = dict[:topK]
		}
		res["source"] = "dictionary"
		res["definitions"] = dict
		res["note"] = "Curated metric definitions. Use expression, required_filters, and exclusions verbatim; do not invent alternative formulas."
		return res
	}
	res["source"] = "inferred"
	res["definitions"] = []MetricDef{}
	res["inferred_candidates"] = c.inferMetricCandidates(metricName, topK)
	res["note"] = "No dictionary entry found. inferred_candidates are naming-convention guesses over catalog columns; confirm the business formula with the user or an operator before treating any of them as authoritative."
	return res
}

type inferredMetric struct {
	Table       string   `json:"table"`
	Column      string   `json:"column"`
	LogicalName string   `json:"logical_name,omitempty"`
	DataType    string   `json:"data_type,omitempty"`
	Description string   `json:"description,omitempty"`
	Expression  string   `json:"suggested_expression"`
	Notes       []string `json:"notes,omitempty"`
	Score       float64  `json:"score"`
}

func (c *Catalog) inferMetricCandidates(metricName string, topK int) []inferredMetric {
	tokens := c.expandTokens(tokenize(metricName))
	var out []inferredMetric
	for _, t := range c.Tables {
		for _, col := range t.Columns {
			matches := scoreColumns(tokens, &Table{Columns: []*Column{col}}, 1)
			score := 0.0
			if len(matches) > 0 {
				score = matches[0].Score
			}
			if score == 0 && !looksMetricColumn(col.Name) {
				continue
			}
			expr, notes := c.metricExpression(t, col)
			if expr == "" {
				continue
			}
			out = append(out, inferredMetric{
				Table:       t.FQN,
				Column:      col.Name,
				LogicalName: col.LogicalName,
				DataType:    col.DataType,
				Description: col.Description,
				Expression:  expr,
				Notes:       notes,
				Score:       round(score + metricNameBonus(metricName, col)),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			if out[i].Table == out[j].Table {
				return out[i].Column < out[j].Column
			}
			return out[i].Table < out[j].Table
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

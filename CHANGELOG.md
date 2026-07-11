# Changelog

## v0.2.0 — 2026-07-11

### Security and DBA controls

- Standalone HTTP remains loopback-only by default. A non-loopback bind now
  requires either meta-DB authentication or both `-public-mcp` and an admin
  token.
- All profile-backed MCP operations (`run_sql_safely`, live `explain_sql`, and
  execution-based `run_evaluation`) share one authorization registry and
  standalone admin-token gate.
- Feedback is server-scoped and quarantined as `pending/untrusted`; only an
  administrator-approved record may influence few-shot examples, retrieval
  priors, or learned rules. Size/rate limits and duplicate suppression are
  included.
- Operator default filters support `enforcement: error` for execution-blocking
  policy, with dialect AST validation and query-block/alias-aware predicate
  checks.

### AI grounding

- Metric resolution now combines exact name/business name/alias priority with
  glossary synonyms and conservative token coverage/proximity scoring.
- Metric lookup, question analysis, and schema retrieval use the same resolver;
  responses include confidence and match evidence.

### MCP and engineering

- Added the administrator-only `review_feedback` tool (29 registered tools).
- Tool registry, dispatcher, and README drift is now detected by tests.
- OpenAPI and MCP server versions share one source of truth.
- Added GitHub Actions checks for module verification, vetting, tests, and all
  CLI builds.

### Upgrade notes

- Existing v1 feedback JSONL records remain audit data but are not trusted or
  learned automatically. Submit new feedback and approve it through
  `review_feedback`.
- `record_feedback` no longer changes retrieval or learning immediately.
- Existing externally bound standalone commands must add `-public-mcp` and
  configure `-admin-token`, or migrate to authenticated `-meta-db` mode.
- Default-filter behavior remains warning-only unless the entry explicitly sets
  `"enforcement": "error"`.

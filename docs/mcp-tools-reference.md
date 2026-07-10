# MCP 도구 레퍼런스 (26종)

모든 도구는 `tools/call`로 호출하며, 결과는 `content[0].text`(JSON 문자열)와
`structuredContent`(동일 객체)로 반환됩니다. 스키마 원본은 `tools/list`가
항상 최신입니다.

## 도구 지도

| 단계 | 도구 |
| --- | --- |
| ① 질문 이해 | `analyze_question`, `resolve_time`, `find_filter_columns` |
| ② 스키마 선택 | `search_schema`, `search_examples`, `get_column_stats` |
| ③ 근거 확보 | `get_metric_definition`, `get_join_paths`, `get_schema_context` |
| ④ SQL 생성 | `build_sql_skeleton` |
| ④ DB 선택 | `list_db_profiles` |
| ⑤ 검증·선택 | `validate_sql`, `rank_candidates`, `explain_sql`, `run_sql_safely` |
| ⑥ 환류 | `record_feedback`, `learn_from_feedback` |
| 운영 | `get_catalog_health`, `run_evaluation`, `suggest_joins`, `list_datasets`, `get_dataset`, `put_dataset`, `remove_dataset`, `reload_catalog` |

---

## ① 질문 이해

### analyze_question

질문을 구조화된 계획으로 분해합니다. **SQL 생성 전 필수 1단계.**

| 파라미터 | 타입 | 설명 |
| --- | --- | --- |
| `question` (필수) | string | 자연어 질문 (한국어/영어) |

응답 주요 필드:

| 필드 | 내용 |
| --- | --- |
| `intent` | `aggregation.count/sum/avg/max/min`, `sort.rank`, `group_by`, `trend`, `list` |
| `intent_signature` | 데이터셋 `target_intent` 어휘와 호환되는 구조 시그니처 (`agg_count_distinct`, `cond_range`, ...) |
| `target_metrics` | 항목별 `{term, source: dictionary\|unknown, definitions[]}` — **dictionary면 계산식을 그대로 사용** |
| `patterns` | 감지된 다단계 SQL 패턴 + CTE 템플릿 (2단 집계, 그룹별 top-N, 전월 대비, 비율, 분포 등) |
| `dimensions` / `filters` / `value_filter_candidates` | 차원, 필터 후보(코드사전 매핑 포함) |
| `time_range` | 파싱된 기간(시작/끝 YYYYMMDD, granularity, comparison) |
| `sort` / `limit` / `comparison` / `aggregation_level` | 정렬·상위N·비교·집계 수준 |
| `expected_output_columns` | 결과에 반드시 있어야 할 항목 → `validate_sql`의 `expected_outputs`로 전달 |
| `ambiguities` / `applied_defaults` | 모호 요소(사용자 확인 필요)와 적용된 기본값(응답에 가정으로 표시) |
| `top_schema_hits` / `fewshot_hits` / `feedback_examples` | 예비 스키마 후보, 유사 예제 |

### resolve_time

시간 표현을 기간으로 파싱하고, 테이블 지정 시 **컬럼 semantic type별 SQL
조건**으로 렌더링합니다.

| 파라미터 | 설명 |
| --- | --- |
| `question` (필수) | 시간 표현 포함 문장 |
| `table` | 지정 시 그 테이블의 날짜 컬럼별 조건 반환 |

지원 표현: 오늘/어제/이번 달/지난달/올해/작년/최근 N개월/최근 N년/YYYY년/
YYYY년 M월/상·하반기/전월 대비/전년 동월 대비/일별·월별·분기별·연도별/원시
날짜. `전월 대비`는 **비교 지시**로만 취급되어 독립 기간을 만들지 않습니다.

렌더링 규칙: `MONTH_YYYYMM` → `base_month BETWEEN '202501' AND '202506'`,
`DATE_YYYYMMDD` → 문자열 범위, `DATE/TIMESTAMP` → `DATE '...'` 반개구간,
`DATETIME_YYYYMMDDHH24MISS` → 14자리 문자열 반개구간.

### find_filter_columns

질문 속 리터럴 값("서울", "정상", "연체", "남자")을 **코드사전·top values·
샘플값**과 대조해 필터 컬럼과 술어를 제안합니다.

| 파라미터 | 설명 |
| --- | --- |
| `values` (필수) | 값 목록 |
| `tables` | 후보 테이블 제한 |
| `top_k` | 기본 8 |

응답: `candidates[] = {value, table, column, matched_in(code_dict/top_values/
sample_values/synonym/logical_name), matched_entry, suggested_predicate,
score}` — 예: `"연체"` → `DLQ_MATRL_TP_CD = '21'`.

---

## ② 스키마 선택

### search_schema

다중 신호 스코어링으로 테이블/컬럼 후보를 반환합니다.

| 파라미터 | 설명 |
| --- | --- |
| `question` (필수) | 질문 또는 검색어 |
| `top_k` | 기본 8 |
| `schemas` | `["dw_history","dw_snapshot"]` 등 스키마 제한 |
| `include_columns` | 컬럼 매칭 포함 여부 |
| `max_columns` | 테이블당 컬럼 수 |

점수 신호: 물리/논리 테이블명, 설명, 도메인 태그, 컬럼(물리/논리/설명/
코드사전/동의어/샘플값), 지표 사전 매핑(+12), 과거 성공 SQL 빈도(로그 부스트),
교정 패널티(학습 룰), 후보 간 조인 연결성(+4), few-shot 대상 일치.

응답: `results[]`(테이블 + `score`, `reasons[]`(매칭 사유), `matched_terms[]`,
`why_include`, `policy_hints[]`, `matched_columns[]`), **`excluded[]`**(컷된
후보와 제외 사유), `glossary_matches`, `expanded_tokens`.

### search_examples

골든 질문-SQL 예제를 검색합니다. lexical 점수에 **intent 시그니처 겹침
(+2/토큰)** 을 더해 같은 SQL 구조의 예제를 우선합니다. 응답 항목에
`intent_overlap[]` 포함.

| 파라미터 | 설명 |
| --- | --- |
| `question` (필수) | 질문 |
| `top_k` | 기본 5 |
| `table` | 대상 테이블 필터 |

### get_column_stats

한 컬럼의 모든 정보: 타입/PK/FK/인덱스/PII/semantic_type/설명/코드사전/동의어
/샘플값 + (column_stats.json 존재 시) row count·null 비율·distinct·min/max·
top values·포맷 패턴·최신성 + 골든 예제 사용 횟수.

| 파라미터 | 설명 |
| --- | --- |
| `table`, `column` (필수) | 대상 |

---

## ③ 근거 확보

### get_metric_definition

**지표 사전 우선** 조회. 응답의 `source`로 신뢰도를 구분합니다.

| 파라미터 | 설명 |
| --- | --- |
| `metric_name` (필수) | 지표/업무 용어 (용어사전으로 확장 매칭) |
| `top_k` | 기본 8 |

- `source: "dictionary"` → `definitions[]`의 `expression`·`required_filters`·
  `exclusions`를 **그대로 사용** (임의 변형 금지)
- `source: "inferred"` → `inferred_candidates[]`는 명명 규칙 기반 **추정**.
  사용자/운영자 확정 전에는 확정 계산식으로 취급 금지

### get_join_paths

조인 그래프에서 모든 테이블 쌍의 경로를 반환합니다. **SQL의 ON 절은 반드시
여기서 취득.**

| 파라미터 | 설명 |
| --- | --- |
| `tables` | 2개 이상 → 모든 쌍 계산 |
| `from_table` + `to_tables` | 대안 지정 방식 |
| `max_depth` | 기본 3 |

응답 경로별: `found`, `depth`, `confidence`(엣지 최소값 − 홉 패널티),
`edges[]`(`condition`, `join_type`, `cardinality`, `preferred`, `caution`),
`guidance`(경로 없음/저신뢰 시 행동 지침), 금지 조인은 차단 사유와 함께 반환.
`overall_guidance`로 전체 판단 제공. **confidence < 0.7이면 SQL을 바로
생성하지 말고 사용자에게 확인하거나 단일 테이블 대안 제시.**

### get_schema_context

선택 테이블의 **압축 컨텍스트**(LLM에 전달할 최소 정보)를 만듭니다.

| 파라미터 | 설명 |
| --- | --- |
| `question` | 컬럼 선별 기준 (tables 생략 시 검색 top5 자동) |
| `tables` | 스키마-한정 테이블 목록 |
| `max_columns_per_table` | 기본 24 |

포함: 테이블(도메인/grain/row count/최신성/기본 필터), 컬럼 Top-K(PK/FK·정책
컬럼 우선, PII 플래그, semantic_type, 코드사전, 통계), **join_conditions**
(선택 테이블 간 검증된 조인), **metrics**(질문 매칭 지표 정의),
**time_conditions**(기간 조건 렌더링), 금지 조인, 생성 규칙(rules),
**excluded_columns**(제외 컬럼과 사유).

---

## ④ SQL 생성

### build_sql_skeleton

검증된 부품만으로 **SQL 골격**을 조립합니다. 복잡/다중 테이블 질문에서
LLM은 `/* SLOT */` 주석만 채웁니다.

| 파라미터 | 설명 |
| --- | --- |
| `question` (필수) | 질문 (시간/지표/패턴 감지에 사용) |
| `tables` | 조인 순서대로. 생략 시 검색 top2 |
| `limit` | LIMIT 값, 기본 1000 |

골격에 자동 포함: alias 부여된 FROM/JOIN(카탈로그 조인 조건), 지표 사전
expression, 최적 날짜 컬럼의 기간 조건(스냅샷/등록일 우선, 속성 날짜
BIRTH/DTH/MTRY 제외, 골든 예제 사용 빈도로 동률 해소), PIT/삭제/EXCL 정책
필터(`COALESCE(is_active, 'Y') <> 'N'` 형태), row bound(`LIMIT n`). 응답에
`aliases`, `join_lines`, `time_alternatives`,
`patterns`(CTE 템플릿), `missing_joins`(+guidance) 포함.

---

## ⑤ 검증·선택

### validate_sql

정적 검증 룰 (Oracle 전용 문법 거부·방언 검사 포함 — NVL/DECODE/ROWNUM 등은
`DIALECT_FUNCTION` 오류, 교차 방언 함수는 대체 함수 제안 경고). 코드 목록과
수정법은 [validation-rules.md](validation-rules.md) 참조.

| 파라미터 | 설명 |
| --- | --- |
| `sql` (필수) | 검증할 SQL |
| `limit` | bounded_sql의 행 제한 |
| `metrics` | 이 SQL이 구현한다고 주장하는 지표명 → expression 일치 검사 |
| `expected_outputs` | `analyze_question.expected_output_columns` → SELECT 커버리지 검사 |

응답: `valid`, `errors[]`/`warnings[]`(코드·메시지·힌트), **`fix_hints[]`**
(구조화 수정 지침 — 자동 수정 루프는 **최대 2회**), `pii_columns[]`,
`referenced_tables/columns`, `bounded_sql`(row bound 자동 부가),
`retry_guidance`. CTE/인라인뷰 스코프 인식(내부의 실제 테이블은 계속 검증).

### rank_candidates

후보 SQL 2~5개를 서버측 객관 점수로 정렬합니다 (LLM 자기평가 대체).

| 파라미터 | 설명 |
| --- | --- |
| `candidates` (필수) | SQL 배열 |
| `question`, `expected_outputs`, `metrics`, `limit` | 검증에 전달 |

점수: 검증 실패 −1000, 오류당 −50, 경고 코드별 차등(−25 스키마누락/지표불일치,
−20 미검증 조인, −15 학습 룰, −5 기타), 리스크×0.5 감점, row bound +10.
응답: `ranking[]`(점수·`deductions[]`), `best_index`, `best_sql`, `guidance`.

### explain_sql

리스크 추정. **정적 분석은 항상**, `profile` 지정 시 **실측 EXPLAIN**
(postgres `EXPLAIN (FORMAT JSON)`, mysql/mariadb `EXPLAIN FORMAT=JSON`)을
추가 수행합니다.

| 파라미터 | 설명 |
| --- | --- |
| `sql` (필수) | 분석할 SQL |
| `limit` | 정적 분석용 행 제한 |
| `profile` | DB 프로파일 id — 대상 DB에서 실제 EXPLAIN(JSON 포맷) 수행 |

정적 응답: `risk`(low/medium/high/blocked), `risk_score`, `risk_factors[]`,
`suggestions[]`, `recommended_action`, `index_hints`. 실측 시 `live_plan`
추가: `dialect`, 플랜 스텝(operation/object/예상행/cost), `total_cost`
(mariadb는 EXPLAIN JSON에 cost가 없어 0), `max_cardinality`, full scan·
카티션·NL 비효율·대형 sort/aggregate·row 과다·고비용 탐지와 보강 제안 —
실측 risk가 high면 `recommended_action`이 `regenerate_with_constraints`로
승격됩니다. 접속·권한 실패 시 `live_plan_error`로 사유 반환. **risk=high면
실행하지 말고 기간/limit 조건을 추가해 재생성.**
상세: [db-connector.md](db-connector.md).

### list_db_profiles

호출자가 사용할 수 있는 DB 접속 프로파일(postgres/mysql/mariadb) 목록을
반환합니다(파라미터 없음). `run_sql_safely`/`explain_sql`/`run_evaluation`의
`profile` 인자로 넘길 id를 발견할 때 사용합니다. 인증 모드에서는
소유·grant·shared 프로파일만(admin은 전체) 반환되고, 비밀번호 참조 등
시크릿은 절대 포함되지 않습니다. 각 항목: `id`, `name`, `type`(DB 종류),
`connect_string`(호스트/DB명), `username`, 정책 요약,
`my_permission`(owner/manage/use). 응답에 `driver_available` 포함 —
순수 Go 드라이버(pgx, go-sql-driver/mysql)가 내장되어 항상 `true`입니다.

### run_sql_safely

검증 + (프로파일 지정 시) **대상 DB(postgres/mysql/mariadb) read-only 실행**.

| 파라미터 | 설명 |
| --- | --- |
| `sql` (필수) | 실행할 SQL |
| `profile` | `db_profiles`의 프로파일 id — 생략 시 dry-run(bounded_sql 반환) |
| `limit` | 행 제한 (프로파일 `max_rows`로 캡) |
| `timeout_seconds` | 프로파일 기본보다 짧을 때만 적용 |

응답 `status`: `dry_run_only`(프로파일 없음) / `blocked`(카탈로그 검증 실패 —
**실행하지 않음**) / `executed`(+`result`: 컬럼 메타, 행, row_count,
elapsed_ms, `truncated`) / `execution_failed`(+정제된 오류: `TIMEOUT`,
`CANCELED`, `PG-<SQLSTATE>`, `MY-<errno>` 코드와 한국어 힌트). 실행은
순수 Go 드라이버로 항상 가능하며 별도 클라이언트 설치나 빌드 태그가
필요 없습니다 — [db-connector.md](db-connector.md) 참조.

---

## ⑥ 환류

### record_feedback

결과를 JSONL로 적재합니다. 성공/교정 SQL은 재기동 시 few-shot 예제와 검색
부스트로 재사용됩니다.

주요 필드: `question`(필수), `outcome`(필수: success/failure/corrected/
rejected), `analysis`, `tables[]`, `columns[]`, `generated_sql`,
`validation_errors`, `final_sql`, `executed`, `adopted`, `duration_ms`,
`result_rows`, `failure_cause`, `notes`.

### learn_from_feedback

반복 실패 패턴을 학습 룰로 승격합니다 (기본 3회 이상).

| 룰 타입 | 원천 | 효과 |
| --- | --- | --- |
| `recurring_error` | 동일 검증 오류(code+table+column) 반복 | 해당 대상 사용 시 예방 경고 |
| `table_correction` | 교정 SQL에서 반복 교체된 테이블 | 검색 점수 패널티 + 대체 테이블 경고 |
| `column_correction` | 반복 교체된 컬럼 | 해당 컬럼 사용 시 대체 컬럼 힌트 |

`learned_rules.json`에 영속화(운영자 편집 가능), 호출 즉시 핫 적용.

---

## 운영 도구

### get_catalog_health

컴파일 상태(`ok/degraded/error`), LoadIssue 전체, 논리명/설명 누락 테이블,
semantic type 미지정 날짜형 컬럼 수, PII 컬럼 목록, 사전 크기, 학습 룰 수.

### run_evaluation

골든셋 평가. `golden_path`(기본 `<data>/golden_queries.json`), `top_k`(기본 5).
지표: `table_selection_acc`, `column_recall_avg`, `metric_lookup_acc`,
`join_path_acc`, `expected_sql_valid`, `avg_response_ms` + 케이스별 미스.
상세: [evaluation.md](evaluation.md).

### suggest_joins

단일 컬럼 PK 마스터를 참조하는 미연결 테이블을 발굴해 N:1 엣지 후보를
근거(FK/인덱스/타입/동시출현)와 함께 제안합니다. `suggested_override`는
overrides.json `preferred_joins`에 붙여넣는 스니펫이며 **자동 적용되지
않습니다**. confidence는 0.85로 캡(운영자 수작업 0.95보다 낮음).

### list_datasets / get_dataset / put_dataset / remove_dataset / reload_catalog

데이터셋 레지스트리(19종)의 조회·교체·제거·리로드. 동작·안전장치는
[datasets.md](datasets.md)와 [rest-api.md](rest-api.md) 참조 — REST API와
동일 코드입니다.

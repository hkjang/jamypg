# 평가 체계

정확도 개선이 실제로 개선인지 수치로 검증하는 체계입니다. DB 연결 없이
결정적으로 동작하므로 CI에서 매 커밋 실행됩니다.

## 실행 방법 3가지

```sh
go test ./...                     # CI — 골든셋 평가 포함, 임계값 미달 시 실패
go run ./cmd/jamypg-eval -verbose # CLI — 케이스별 미스 출력
# MCP: run_evaluation {golden_path?, top_k?}
```

`jamypg-eval` 플래그: `-data`(기본 data/metadb), `-golden`(기본
`<data>/golden_queries.json`), `-top-k`(기본 5), `-verbose`, `-profile`
(실행 기반 평가용 DB 프로파일 id).
table_selection_acc < 0.7이면 종료 코드 1 (파이프라인 게이트용).

## 측정 지표

| 지표 | 정의 | 현재 임계값(테스트) |
| --- | --- | --- |
| `table_selection_acc` | 기대 테이블 중 하나라도 검색 top-k에 든 케이스 비율 | ≥ 0.85 |
| `column_recall_avg` | 기대 컬럼이 매칭 컬럼에 나타난 평균 재현율 | ≥ 0.8 |
| `metric_lookup_acc` | 기대 지표가 사전에서 조회되는 비율 | = 1.0 |
| `join_path_acc` | 다중 테이블 케이스에서 모든 쌍의 조인 경로 존재 비율 | ≥ 0.85 |
| `expected_sql_valid` | 기대 SQL이 `validate_sql`을 통과하는 비율 | = 1.0 |
| `avg_response_ms` | 케이스당 평균 처리 시간 | (참고용) |

임계값은 80케이스 실측(table 0.94 / column 0.90 / join 0.94)보다 약간
낮게 잡아 **회귀는 잡고 플레이크는 방지**합니다. 정확도를 크게 올린 뒤에는
임계값도 함께 올려 개선을 고정하십시오 (`internal/catalog/eval_test.go`).

## 골든셋 형식 — `golden_queries.json`

```json
[{"id": 2,
  "question": "2025년 6월 기준 평균 신용점수를 등급별로 보여줘",
  "expected_tables": ["dw_snapshot.customer_snapshot"],
  "expected_columns": ["credit_score", "credit_grade", "base_month"],
  "expected_metrics": ["평균 신용점수"],
  "expected_sql": "SELECT ... LIMIT 100",
  "note": "curated"}]
```

- `expected_tables`: 스키마-한정 이름. 2개 이상이면 조인 경로도 평가됨
- `expected_columns`: 베어 컬럼명 또는 `TBL.COL` (베어로 정규화되어 비교)
- `expected_sql`: 있으면 검증기 통과 여부를 측정 — **검증을 통과하는 SQL만**
  넣으세요 (데이터셋 오타가 지표를 오염시키지 않도록)
- `note`: 자동 생성 케이스는 출처(sql_datasets id, 도메인, 난이도)가 기록됨

## 골든셋 확장 — `jamypg-goldgen`

`sql_datasets.json`(7,583건)에서 대표 케이스를 자동 선별합니다.

```sh
go run ./cmd/jamypg-goldgen -data ./data/metadb -n 80 -keep 5
```

동작:

1. 기존 골든셋 상단 `-keep`개(수작업 케이스)를 보존
2. 후보 필터: 질문·SQL 비어있지 않음, **대상 테이블이 카탈로그에 전부
   실존**, 중복 질문 제외
3. `expected_columns`는 카탈로그 실존 검증 후 채택(최대 6),
   `expected_metrics`는 지표 사전 매칭으로 도출
4. `expected_sql`은 정적 검증을 통과한 경우에만 포함
5. **도메인 × 난이도 버킷 층화** 라운드로빈으로 `-n`까지 선별 (특정 업무
   영역 쏠림 방지, 결정적 순서)

권장 운영: 분기마다 재생성 + 새 업무 질문을 수작업 케이스로 상단에 추가
(`-keep` 값 증가).

## 미스 분석 워크플로

```sh
go run ./cmd/jamypg-eval -verbose | grep -A3 MISS
```

| 미스 유형 | 흔한 원인 | 해결 |
| --- | --- | --- |
| `table:...` | 논리명/설명에 질문 어휘 없음 | glossary 동의어 추가, overrides로 테이블 설명 보강 |
| `column:...` | 컬럼 논리명 부재/불일치 | overrides.columns.synonyms |
| `metric:...` | 지표 별칭 누락 | metrics.json aliases 추가 |
| `join:A->B` | 조인 그래프 공백 | `suggest_joins` → preferred_joins 추가 |
| `sql_error:...` | 기대 SQL이 검증 위반 | 기대 SQL 수정 또는 검증 룰 오탐 확인 |

실사례: 80케이스 도입 직후 join 미스가 전부 `TBKP94A`(거래기관 마스터)
엣지 부재였고, preferred_joins 4건 추가로 join_path_acc 0.86 → 0.94.

## CI 통합

`go test ./...`가 곧 게이트입니다. 골든셋 파일이 없으면 해당 테스트는
skip되므로, 파이프라인에서는 데이터 포함 체크아웃을 사용하세요.
평가 소요는 80케이스 기준 약 13초입니다.

## 결과 기반 평가 (실 DB 연결 시)

DB 프로파일을 지정하면 골든셋의 `expected_sql`을 실제로 실행해 두 지표를
추가 측정합니다 (`SELECT COUNT(*) FROM (...) AS jamypg_q` 래핑 —
read-only 가드·타임아웃 적용):

```sh
go run ./cmd/jamypg-eval -data ./data/metadb -profile pg-prod-01
# MCP: run_evaluation {profile: "pg-prod-01"}
```

| 추가 지표 | 정의 |
| --- | --- |
| `execution_success_rate` | 정적 검증을 통과한 expected_sql이 오류 없이 실행되는 비율 |
| `row_sanity_rate` | 결과 건수가 `expected_min_rows`~`expected_max_rows` 범위인 비율 |

골든 케이스에 범위를 지정하는 법 (둘 다 선택; 없으면 실행 성공만 확인):

```json
{"question": "...", "expected_sql": "...",
 "expected_min_rows": 1, "expected_max_rows": 100000}
```

케이스별 결과에는 `executed_rows`, `row_sanity_ok`, `exec_error`가 담기므로
"문법은 맞는데 0행/폭주"인 의미 오류를 골든셋 수준에서 잡을 수 있습니다.
실행 요건: 대상 DB(`postgres`/`mysql`/`mariadb`) 프로파일만 있으면 됩니다 —
드라이버가 순수 Go라 별도 빌드 태그·클라이언트가 필요 없습니다
([db-connector.md](db-connector.md)).

## 통합 테스트 환경 (deploy/test)

세 방언 전부를 실 DB로 검증할 수 있는 로컬 테스트 환경이 포함되어 있습니다.
`deploy/test/docker-compose.yml`이 jamypg 메타 스키마를 text2sql 대상으로
시드한 3개 컨테이너를 띄웁니다:

| 컨테이너 | 이미지 | 포트 | DB |
| --- | --- | --- | --- |
| postgres-meta | postgres:16 | 55432 | `jamypg_meta` (라이브 메타 DB 겸용) |
| mysql-meta | mysql:8.4 | 53306 | `public` |
| mariadb-meta | mariadb:11.4 | 53307 | `public` |

```sh
docker compose -f deploy/test/docker-compose.yml up -d
go test -tags integration ./test/integration -v
```

카탈로그 데이터셋은 `data/metadb`(메타 스키마 기반, PostgreSQL 방언)를
사용합니다. 스키마·시드가 바뀌면 `python3 deploy/test/gen_testenv.py`로
픽스처를 재생성하세요.

## 향후 확장

- 결과 체크섬 비교 (row 수를 넘어 값 검증)
- 난이도(Level)별·intent별 정확도 리포트 분해

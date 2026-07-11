# 자동 메타데이터 수집 (Automated Metadata Sync)

대상 소스 DB(PostgreSQL·MySQL·MariaDB)의 **물리 메타데이터를 자동 수집**하고,
버전 스냅숏으로 보관하며, 스냅숏 간 **증분 변경**을 감지하는 기능입니다.
`자동 메타데이터 관리(FR-META-001..005)` 스펙의 **Phase 1-2(수집 + 스냅숏 +
증분 변경감지)** 구현이며, 코드는 `internal/metasync` 패키지에 있습니다.

## 개요

- **입력**: DB 프로파일(`db_profiles.json`)로 등록된 소스 DB — 커넥터가 이미
  쓰는 read-only 풀·서킷브레이커를 그대로 재사용합니다([db-connector.md](db-connector.md)).
- **수집**: 소스별 물리 구조(스키마·테이블·뷰·컬럼·제약·인덱스·주석·행수 추정)를
  방언별 시스템 카탈로그 조회로 읽어 공통 자산 모델로 정규화합니다.
- **보관**: 매 수집마다 버전이 매겨진 `RawSnapshot`을 JSON으로 저장합니다.
- **변경감지**: 두 스냅숏을 비교해 변경 유형·심각도·처리방침(disposition)을
  담은 `ChangeSet`을 생성합니다.
- **인터페이스**: MCP 도구 5종 + REST 엔드포인트 5종.

## 핵심 원칙 — 물리는 자동, 의미는 승인

> **물리 메타데이터는 자동으로 수집되지만, 업무 의미(논리명·지표·조인 정책)는
> 오직 "검토 가능한 후보"로만 생성되며 이 기능이 운영 카탈로그에 직접 기록하는
> 일은 절대 없습니다.**

수집·파생되는 모든 값 모델은 **출처(provenance)** 를 함께 지닙니다
(`types.go`의 `Provenance`). 이는 값이 어떻게 만들어졌고, 어떤 근거로, 얼마나
확신되는지를 설명·감사 가능하게 합니다.

| 필드 | 의미 |
| --- | --- |
| `confidence` | 0..1 확신도. 시스템 카탈로그에서 읽은 물리 사실은 1.0 |
| `evidence` | 사람이 읽을 수 있는 근거 목록 |
| `generator` | 생성 방식: `system_catalog` / `rule` / `statistics` / `llm` / `user_feedback` |
| `model_id` | LLM 사용 시 모델 식별자 |
| `review_status` | 승인 상태: `discovered` / `suggested` / `approved` / `rejected` / `retired` |
| `reviewer` | 검토자 |

물리 수집(Phase 1-2)은 전부 `generator=system_catalog`, `confidence=1.0`에
해당합니다. 후속 단계(규칙·통계·LLM 파생)를 위한 후보·승인 모델은 이미
`types.go`에 자리를 잡아 두었습니다(아래 [후속 단계](#이번-단계-범위와-후속-단계-phase-3-10) 참조).

## 수집 대상

프로파일(소스 DB)별로 다음을 공통 자산 모델(`TableAsset`/`ColumnAsset`/
`ConstraintAsset`/`IndexAsset`)로 수집합니다.

| 대상 | 세부 | 비고 |
| --- | --- | --- |
| 스키마 | 비시스템 스키마 | `pg_catalog`·`information_schema`·`pg_toast`·`mysql`·`performance_schema`·`sys` 제외 |
| 테이블 | 이름, 종류(`table`) | |
| 뷰 / 머티리얼라이즈드 뷰 | 종류(`view`/`materialized_view`), 뷰 SQL | **opt-in**: `include_views=true`일 때만 |
| 컬럼 | 이름, 순서(ordinal), `data_type`, `full_type`(길이/정밀도 포함, 예 `varchar(64)`), nullable, 기본값, 생성식(generation expression), 주석 | |
| 제약 | PK / FK / UNIQUE / CHECK — 컬럼, FK 참조 대상(스키마·테이블·컬럼), CHECK 정의 | **항상 수집** |
| 인덱스 | 이름, 컬럼, unique, primary 여부 | **항상 수집** |
| 테이블 주석 | | |
| 행수 추정 | `est_row_count` | 카탈로그 추정치(정확값 아님) |

- **뷰는 옵트인**(`include_views`), **인덱스·제약은 항상 수집**됩니다. 구조
  해시(structural hash)를 완전하고 안정적으로 만들기 위해서입니다.
- 컬럼의 PK/FK 플래그(`is_primary_key`/`is_foreign_key`)는 수집된 제약으로부터
  파생됩니다.

## Read-only 계정과 시스템 카탈로그 접근

스펙의 **"수집 권한: 시스템 카탈로그 조회 전용 계정"** 요건을 지키기 위해,
컬렉터는 SELECT 권한만 가진 read-only 계정으로도 제약을 읽을 수 있는 경로를
사용합니다. 핵심 함정은 다음과 같습니다.

- **`information_schema.table_constraints`는 read-only 계정에게 보이지 않습니다.**
  PostgreSQL·MariaDB에서 이 뷰로 특정 테이블의 제약을 나열하려면
  INSERT/UPDATE/REFERENCES 등의 권한이 필요하기 때문입니다.

따라서 컬렉터는 방언별로 우회합니다.

| 방언 | 제약 수집 방식 (`collector_pg.go` / `collector_mysql.go`) |
| --- | --- |
| PostgreSQL | 제약을 **`pg_catalog.pg_constraint`** 에서 직접 읽음(어떤 롤이든 조회 가능). FK 참조 대상은 `confkey`↔`pg_attribute` 매핑으로 복원 |
| MySQL / MariaDB | PK·UNIQUE를 **`information_schema.statistics`**(인덱스 메타데이터, `non_unique=0`)에서 파생 — read-only 계정도 조회 가능. `PRIMARY` 인덱스 → PRIMARY KEY, 그 외 unique 인덱스 → UNIQUE |
| MySQL / MariaDB (FK) | `information_schema.key_column_usage`로 **best-effort**. MariaDB의 read-only 계정은 참조 행이 보이지 않을 수 있어, 이때 FK는 수집되지 않음(실패가 아니라 **문서화된 권한 한계**) |

인덱스는 두 방언 모두 read-only 계정이 조회 가능한 카탈로그(PostgreSQL
`pg_index`, MySQL/MariaDB `information_schema.statistics`)에서 읽습니다.
스키마 탐색(`discover_metadata`)과 컬럼 조회는 `information_schema`만 사용합니다.

## 스냅숏 · 구조 해시 · 증분 스킵

### RawSnapshot

매 수집은 버전이 매겨진 `RawSnapshot`을 만듭니다.

- `snapshot_id`, `source_id`, `dialect`, `collector_version`(현재 `1.0.0`),
  `collected_at`, `schema_hash`, `status`(`success`/`partial`/`failed`),
  `tables[]`
- `object_count`: `{schemas, tables, views, columns, constraints, indexes}`

**저장 위치**: `<dataDir>/metasync/snapshots/<source>/<snapshot_id>.json`

메타 DB 없이 **standalone 모드에서도 동작**하도록 파일 기반으로 저장하며,
audit/feedback/backups 저장 방식을 그대로 따릅니다. 목록 조회 시에는 무거운
`tables[]` 본문을 제외한 요약만 반환합니다.

### 구조 해시(StructHash / SchemaHash)

- 테이블마다 **구조 정의에만** 대한 `StructHash`를 계산합니다: 스키마·이름·종류,
  순서 있는 컬럼(타입·nullable·PK/FK·기본값·생성식), 제약, 인덱스, 뷰 SQL.
- **주석과 행수 추정은 해시에 포함하지 않습니다.** 주석·행수 변동(churn)이 거짓
  "구조 변경"으로 잡히지 않게 하기 위함입니다.
- 스냅숏의 `schema_hash`는 모든 테이블 해시를 합쳐 만든 소스 단위 해시입니다.

### 증분 스킵 (FR-META-005)

- `Sync(incremental=true)` — **기본값**: 새로 계산한 `schema_hash`가 최신 스냅숏의
  것과 같으면 **중복 스냅숏을 저장하지 않고 스킵**합니다(`skipped=true`).
- `incremental=false`: 항상 스냅숏을 저장하고 diff를 계산합니다.

## 변경 감지 (ChangeSet)

`Diff(from, to)`는 기준(baseline) 스냅숏과 현재 스냅숏을 비교해 `ChangeSet`을
만듭니다. **구조 해시가 동일한 테이블은 건너뜁니다.** `ChangedTables`에는 실제로
변경된 테이블만 담기며, 이는 증분 재컴파일의 입력이 됩니다.

### 처리방침(disposition) — 삭제는 즉시 반영하지 않음 (AC-02)

**핵심**: 삭제는 절대 즉시 적용되지 않습니다. 제거된 테이블/컬럼은
`retire_candidate`(폐기 후보)가 되어, 스튜어드가 승인하기 전까지 운영 카탈로그가
계속 서비스합니다. 추가는 `create_candidate`, 위험한 변경은 `review`입니다.

| 변경 유형(kind) | 심각도(severity) | disposition |
| --- | --- | --- |
| `table_added` | low | `create_candidate` |
| `table_removed` | **breaking** | **`retire_candidate`** |
| `column_added` | low | `create_candidate` |
| `column_removed` | **breaking** | **`retire_candidate`** |
| `type_changed` | medium(같은 기본타입, 크기/정밀도만) / high(기본타입 변경) | `review` |
| `nullability_changed` | medium | `review` |
| `key_changed` (PK/FK) | high | `review` |
| `comment_changed` | low | `update_candidate` |
| `index_changed` | low | `update_candidate` |
| `view_sql_changed` | high | `review` |

`ChangeSet`은 `changes[]`, `changed_tables[]`, 그리고 유형별 카운트
`summary`(map)를 포함합니다.

## MCP 도구 (5종)

코드: `internal/mcp/metasync.go`, 등록·라우팅: `internal/mcp/server.go`.

| 도구 | 인자 | 동작 |
| --- | --- | --- |
| `list_metadata_sources` | (없음) | 수집 소스로 쓸 수 있는 DB 프로파일 목록(`list_db_profiles`와 동일한 권한 필터 적용) |
| `discover_metadata` | `source` | 소스의 비시스템 스키마 나열(read-only, `information_schema`만) |
| `run_metadata_sync` | `source`, `schemas?`, `incremental?=true`, `include_views?=false` | 수집 + 스냅숏 + 이전 스냅숏 대비 변경 집합 반환. 증분이면 변경 없을 때 스킵 |
| `get_sync_status` | `source` | 저장된 스냅숏 목록(최신순) — `collected_at`, `schema_hash`, `object_count` |
| `diff_metadata_snapshots` | `source`, `from`, `to` | 저장된 두 스냅숏 id 간 변경 집합 |

### 권한(Authorization)

- `discover_metadata` / `run_metadata_sync`는 DB에 접속하므로 `dbProfileTools`
  (+ `probesAll`)에 속합니다. **standalone HTTP에서는 admin 토큰**이 필요하고,
  **인증 모드에서는 프로파일별 권한 필터**(per-profile ACL)를 적용합니다.
- `list_metadata_sources` / `get_sync_status` / `diff_metadata_snapshots`는
  저장된 스냅숏과 프로파일 목록을 읽습니다. `list_metadata_sources`는 사용
  가능한 프로파일로 권한 필터링됩니다.

## REST 엔드포인트

코드: `internal/mcp/dbapi.go`. 모두 `requireQueryActor`를 거치며, source가 있는
엔드포인트는 추가로 `canUseProfileID`로 해당 소스 사용 권한을 확인합니다.

| 메서드·경로 | 바디/파라미터 |
| --- | --- |
| `GET /api/metadata/sources` | — |
| `POST /api/metadata/discover` | `{source}` |
| `POST /api/metadata/sync` | `{source, schemas?, incremental?, include_views?}` |
| `GET /api/metadata/snapshots/{source}` | (경로 파라미터 `source`) |
| `POST /api/metadata/diff` | `{source, from, to}` |

## 사용 예시

`run_metadata_sync`로 수집·스냅숏을 만든 뒤, 두 스냅숏 사이를 diff합니다.

```jsonc
// 1) 최초 수집 (기준 스냅숏 생성)
run_metadata_sync { "source": "pg-prod-01", "schemas": ["public"] }
// → { status: "ok", snapshot: { snapshot_id: "snap-...", schema_hash: "...", object_count: {...} },
//     skipped: false, baseline: "", changed_tables: [...], change_summary: {...},
//     principle: "물리 구조는 스냅숏으로 자동 수집되었습니다. 삭제는 ... 폐기 후보로 ..." }

// 2) 소스 DDL 변경 후 재수집 (증분 — 변경이 있으면 새 스냅숏 저장 + 변경집합 반환)
run_metadata_sync { "source": "pg-prod-01" }
// → 변경 없으면 skipped:true (중복 스냅숏 미저장)

// 3) 저장된 스냅숏 확인
get_sync_status { "source": "pg-prod-01" }
// → { snapshots: [ { snapshot_id, collected_at, schema_hash, object_count }, ... ] }  // 최신순

// 4) 두 스냅숏 간 변경 비교
diff_metadata_snapshots { "source": "pg-prod-01", "from": "snap-A", "to": "snap-B" }
// → { changed_tables: [...], change_summary: { column_added: 2, ... },
//     changes: [ { kind, severity, table, column, before, after, disposition }, ... ] }
```

REST로는 `POST /api/metadata/sync` → `POST /api/metadata/diff` 순으로 동일하게
수행합니다.

## 이번 단계 범위와 후속 단계 (Phase 3-10)

이 문서가 다루는 것은 **Phase 1-2: 수집 + 스냅숏 + 증분 변경감지**입니다. 더 큰
`자동 메타데이터 관리` 스펙에서 아래 항목은 **아직 구현되지 않았습니다.**

- 데이터 프로파일링(값 분포, NULL 비율 등 통계)
- AI 의미 보강(논리명·설명·시맨틱 타입)
- 코드 사전 / 지표(metric) / 관계(relation) 후보 생성
- 리니지(lineage)
- 품질 점수(quality scoring)
- 승인 워크플로 UI
- 스케줄러

다만 후보·출처·승인 **모델은 이미 `types.go`에 자리 잡혀 있어**(Generator,
ReviewStatus, Provenance) 위 단계들이 그 위에 얹히도록 설계되어 있습니다.
로드맵상 **Phase 3-10**은 후속 작업으로 남아 있습니다.

## 참고

- [db-connector.md](db-connector.md) — 소스 DB 접속·풀·서킷브레이커(수집이 재사용)
- [mcp-tools-reference.md](mcp-tools-reference.md) — 전체 MCP 도구 레퍼런스
- [architecture.md](architecture.md) — 카탈로그 컴파일 파이프라인
- 코드: `internal/metasync/{types,collector,collector_pg,collector_mysql,diff,service}.go`,
  `internal/mcp/{metasync,server,dbapi}.go`

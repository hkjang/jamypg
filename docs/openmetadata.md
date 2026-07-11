# OpenMetadata 연동

jamypg를 [OpenMetadata](https://open-metadata.org)와 양방향 연동해 메타데이터
관리를 자동화합니다. OpenMetadata는 조직이 큐레이션한 업무 메타데이터(설명·
displayName·PII 태그·용어집·오너십)의 중앙 저장소이고, jamypg는 이를 NL2SQL에
활용합니다. 두 시스템을 연결하면 **논리명·설명·PII 분류를 손수 입력하지 않고
전사 카탈로그에서 자동 수급**하고, 반대로 jamypg가 생성한 메타데이터를 다시
채워 넣을 수 있습니다.

## 원칙

- **물리 사실은 자동, 업무 의미는 검토 후 반영** — OpenMetadata의 업무 메타데이터
  는 jamypg에 **후보**로 들어오며 기본은 미리보기입니다. 실제 반영(overrides/
  glossary 병합)은 `apply=true`라는 명시적 두 번째 행위로만 일어나고, 각 파일은
  자동 백업되며 **운영자가 이미 채운 값은 절대 덮어쓰지 않습니다**(빈 필드만).
- **읽기 전용 존중** — export도 OpenMetadata에 **이미 설명이 있는 컬럼은 건드리지
  않고** 빈 필드만 채웁니다. 기본은 dry-run입니다.

## 설정

```sh
export JAMYPG_OPENMETADATA_URL=http://openmetadata:8585   # 콘솔/URL 또는 .../api
export JAMYPG_OPENMETADATA_TOKEN=<bot-jwt>                 # OpenMetadata bot JWT
go run ./cmd/jamypg-mcp -data ./data/metadb -addr 127.0.0.1:9797
# 또는 플래그: -openmetadata-url ... -openmetadata-token ...
```

봇 토큰은 OpenMetadata의 *Settings → Bots*에서 발급합니다(예: `ingestion-bot`).

## 관리 콘솔 (curl 없이)

좌측 내비 **🔗 OpenMetadata** (`/admin/openmetadata`)에서 GUI로 운영할 수 있습니다:

- **연결 상태 확인** — 설정·연결·서버 버전 표시
- **Import**: scope/max/용어집 포함 선택 → *미리보기*(후보 테이블) → *반영 + 리로드*
  (확인 다이얼로그, 컬럼 후보·PII 배지·skipped 표시)
- **Export**: *계획(dry-run)* → *실제 반영*(변경 계획·기록 상태 표)
- 하단에 원본 JSON 응답 표시

REST/MCP를 그대로 호출하므로 아래 API와 동작·안전장치가 동일합니다.

## Import (OpenMetadata → jamypg)

OpenMetadata의 테이블/컬럼 메타데이터를 가져와 jamypg 카탈로그의 **빈 필드에만**
후보로 매핑합니다.

| OpenMetadata | → jamypg |
| --- | --- |
| column `displayName` | 컬럼 논리명(logical_name) |
| column `description` | 컬럼 설명(description) |
| column tag `PII.Sensitive` | `pii: true` + semantic_type `PII` |
| table `displayName` / `description` | 테이블 오버라이드 |
| glossaryTerms | glossary.json 용어 |

테이블은 OpenMetadata FQN(`service.database.schema.table`)의 뒤 2개 세그먼트를
jamypg의 `schema.table`로 축약해 매칭합니다. 카탈로그에 없는 테이블은
`skipped_tables`로 보고됩니다.

```sh
# 미리보기 (기본): 후보만 반환, 파일 미변경
curl -s -X POST http://127.0.0.1:9797/api/openmetadata/import \
  -H 'Content-Type: application/json' -d '{"scope":"svc.metadb","max_tables":500}'

# 반영 (관리자): overrides.json/glossary.json 병합 + 백업 + 카탈로그 리로드
curl -s -X POST http://127.0.0.1:9797/api/openmetadata/import \
  -H 'Content-Type: application/json' -d '{"apply":true}'
```

MCP: `import_openmetadata{scope?, max_tables?, include_glossary?, apply?}`.
멱등 — 재실행 시 이미 채워진 값은 건너뜁니다.

## Export (jamypg → OpenMetadata)

jamypg가 가진 컬럼 설명(명시적 설명, 없으면 논리명으로 조합)을 OpenMetadata의
**설명이 비어 있는 컬럼**에 JSON Patch로 씁니다.

```sh
# 계획 (기본): 무엇을 쓸지만 반환
curl -s -X POST http://127.0.0.1:9797/api/openmetadata/export \
  -H 'Content-Type: application/json' -d '{"dry_run":true}'

# 실제 반영 (관리자)
curl -s -X POST http://127.0.0.1:9797/api/openmetadata/export \
  -H 'Content-Type: application/json' -d '{"dry_run":false}'
```

MCP: `export_to_openmetadata{scope?, max_tables?, dry_run?}`.

## 자동화 (스케줄러 연계)

주기적 워크플로 예시:

```
매일 새벽:
  run_metadata_sync            # 물리 구조 변경 감지(내장 스케줄러 -sync-interval)
  import_openmetadata apply    # OM의 신규 업무 메타데이터를 빈 필드에 반영
  get_metadata_quality gate    # 품질 게이트 확인
  → 다이제스트 웹훅으로 결과 통지(-digest-webhook)
```

`openmetadata_status`(MCP/`GET /api/openmetadata/status`)로 연결·인증·버전을
먼저 확인하세요.

## 매핑 세부 / 안전장치

- **PII**: OpenMetadata 기본 분류 `PII.Sensitive`만 민감으로 취급하며
  (`PII.NonSensitive`/`Tier.*`는 제외), import 시 `pii=true`로 표시되어 jamypg의
  PII 마스킹·차단 정책이 자동 적용됩니다.
- **덮어쓰기 금지**: import는 jamypg가 비어 있는 필드에만, export는 OpenMetadata가
  비어 있는 컬럼에만 씁니다. 양쪽의 사람 큐레이션을 보존합니다.
- **백업**: import apply는 `overrides.json`/`glossary.json`을 반영 전
  `<data>/backups/`에 백업합니다.
- **감사**: import apply / export 쓰기는 감사 로그(해시 체인)에 기록됩니다.

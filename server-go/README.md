# 😜 Emoji Chat

말은 필요 없어, 이모지면 충분해.

이모지로만 대화하는 실시간 채팅 웹서비스입니다.

## 주요 기능

- **이모지 전용 채팅** — 서버/클라이언트 양쪽에서 이모지만 허용
- **UUID 기반 채팅방** — 방 생성 시 고유 UUID 발급, 방장(owner) 개념 있음
- **로그인 없음** — localStorage에 UUID 사용자 ID 자동 생성·관리
- **QR 코드 초대** — 채팅방 QR 코드를 오프라인에서 공유하여 접속
- **실시간 메시징** — WebSocket 기반, 자동 재연결 (exponential backoff)
- **채팅 기록 저장** — SQLite 또는 MySQL에 영구 저장, 이전 메시지 불러오기 지원

## 기술 스택

| 구분 | 기술 |
|------|------|
| Backend | Go 1.23+, `gorilla/websocket`, `go-sql-driver/mysql`, `go-sqlite3` |
| Frontend | HTML / CSS / JavaScript (프레임워크 없음) |
| Database | SQLite (기본) 또는 MySQL 5.7+ (utf8mb4) |
| QR 생성 | [qrcode.js](https://github.com/davidshimjs/qrcodejs) (CDN) |

## 프로젝트 구조

```
emoji_chat/
├── README.md             # 전체 프로젝트 README (Go 버전 기준)
├── server-go/            # 본 디렉토리 — Go 1.23 + gorilla/websocket
│   ├── main.go           # 백엔드 전체 (HTTP API, WebSocket Hub, 이모지 검증)
│   ├── go.mod / go.sum
│   ├── schema.sql        # MySQL DDL
│   └── static/
│       ├── index.html    # SPA 프론트엔드
│       ├── style.css     # 다크 테마 UI
│       └── app.js        # 클라이언트 로직 (라우팅, WebSocket, QR)
├── server-kotlin/        # Kotlin 1.9 + Spring Boot 3.3 버전
└── server-elixir/        # Elixir + Phoenix 1.7 (Bandit + WebSockAdapter) 버전
```

세 서버는 같은 REST/WebSocket 인터페이스를 노출하므로 각자의 `static/` 디렉토리에 들어 있는
`index.html` / `app.js` / `style.css` 가 그대로 호환됩니다. 자세한 실행 방법과 설계 메모는
`server-kotlin/README.md`, `server-elixir/README.md` 를 참고하세요.

## 시작하기

### 1. Go 의존성 설치

```bash
go mod tidy
```

### 2. 서버 실행

#### SQLite (기본, 설정 불필요)

```bash
go run main.go
```

자동으로 `emoji_chat.db` 파일이 생성되고 테이블이 초기화됩니다.

#### MySQL 사용 시

```bash
mysql -u root < schema.sql
DB_TYPE=mysql go run main.go
```

커스텀 DSN:

```bash
DB_TYPE=mysql MYSQL_DSN="user:password@tcp(host:3306)/emoji_chat?charset=utf8mb4&parseTime=true&loc=UTC" go run main.go
```

### 3. 접속

브라우저에서 http://localhost:8081 을 열면 됩니다.

## API 엔드포인트

| Method | Path | 설명 |
|--------|------|------|
| `POST` | `/api/rooms` | 새 채팅방 생성 |
| `GET` | `/api/rooms/{id}` | 채팅방 정보 조회 |
| `DELETE` | `/api/rooms/{id}` | 채팅방 삭제 (방장만 가능, `X-User-Id` 헤더 필요) |
| `GET` | `/api/rooms/{id}/messages` | 메시지 목록 조회 (`?limit=50&before=msgId`) |
| `GET` | `/ws/{roomId}` | WebSocket 연결 (`?userId=uuid`) |

## .env 파일

서버는 시작 시 같은 디렉토리의 **`.env`** (그리고 있다면 `.env.local`) 를 자동으로 로드합니다.
**OS 환경변수가 이미 설정되어 있으면 그쪽이 우선**이라 운영에서는 `.env` 없이 systemd 등에서
환경을 주입해도 동일하게 동작합니다.

```bash
cp .env.example .env
# 그 후 .env 안의 SESSION_SECRET 등을 채움
go run main.go
```

`SESSION_SECRET` 한 줄만 채워도 기본값 (SQLite + loopback bind + 8081 포트) 으로 바로 뜹니다.

## 환경변수

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `DB_TYPE` | `sqlite` | 데이터베이스 종류 (`sqlite` 또는 `mysql`) |
| `SQLITE_PATH` | `emoji_chat.db` | SQLite 파일 경로 (`DB_TYPE=sqlite`일 때) |
| `MYSQL_DSN` | `root:@tcp(127.0.0.1:3306)/emoji_chat?charset=utf8mb4&parseTime=true&loc=UTC` | MySQL 연결 문자열 (`DB_TYPE=mysql`일 때) |
| `PORT` | `8081` | 서버 포트 |
| `BIND_ADDR` | `127.0.0.1` | listen 할 네트워크 인터페이스. 기본은 loopback (외부 비노출, Cloudflare Tunnel / reverse proxy 와 같이 쓰는 안전한 기본값). 모든 인터페이스에서 받으려면 `0.0.0.0` |
| `PUBLIC_ORIGIN` | (빈 값) | QR 코드 / 초대 URL 에 쓰일 외부 origin. 비어있으면 브라우저의 `location.origin` 사용. 예: `https://chat.acidblob.com` |
| `SESSION_SECRET` | **필수** | HMAC 세션 서명용 시크릿. 32자 이상의 랜덤 문자열. 예: `openssl rand -hex 32`. 서버 재시작 사이에 동일하게 유지해야 기존 세션이 살아남음 |

## DB 스키마

- **rooms** — `id` (UUID), `owner_id`, `name`, `created_at`
- **messages** — `id` (AUTO_INCREMENT), `room_id`, `user_id`, `content`, `created_at`

모든 테이블은 `utf8mb4` 문자셋을 사용하여 이모지를 완전히 지원합니다.

## 사용 흐름

1. 홈에서 **새 채팅방 만들기** 클릭
2. 채팅방에서 📱 버튼으로 **QR 코드** 생성
3. 상대방이 QR 코드를 스캔하거나 URL을 붙여넣어 **채팅방 참여**
4. 이모지로 대화!

# 😜 Emoji Chat — Phoenix (Elixir)

Go 버전과 동일한 동작을 Elixir + Phoenix 로 재구현했습니다. WebSocket
은 Phoenix Channels 가 아니라 `WebSockAdapter` 기반 raw WebSocket 으로
구현되어 있어, 기존 `app.js` 가 그대로 동작합니다.

## 기술 스택

| 구분 | 기술 |
|------|------|
| Language | Elixir 1.15+ on Erlang/OTP 26+ |
| Framework | Phoenix 1.7 (Bandit 어댑터) |
| WebSocket | `WebSockAdapter` (raw WS) + `Phoenix.PubSub` |
| Persistence | Ecto + `ecto_sqlite3` (기본) / `myxql` |
| Frontend | Go 버전과 동일 (`priv/static/`) |

## 디렉토리

```
server-elixir/
├── mix.exs
├── config/
│   ├── config.exs
│   ├── dev.exs / test.exs
│   └── runtime.exs           # DB_TYPE, MYSQL_*, SQLITE_PATH, PORT
├── lib/
│   ├── emoji_chat/
│   │   ├── application.ex    # PubSub + RoomRegistry + RoomSup + Repo + Endpoint
│   │   ├── repo.ex
│   │   ├── room.ex / message.ex          # Ecto schemas
│   │   ├── chat.ex                       # context (CRUD + serialize)
│   │   ├── emoji_validator.ex            # 이모지 전용 검증
│   │   └── room_presence.ex              # 방별 GenServer (join/leave/idle)
│   ├── emoji_chat_web.ex
│   └── emoji_chat_web/
│       ├── endpoint.ex                   # Plug.Static (/static), Router
│       ├── router.ex
│       ├── controllers/
│       │   ├── room_controller.ex        # POST/GET/DELETE /api/rooms
│       │   ├── message_controller.ex     # GET /api/rooms/:id/messages
│       │   ├── page_controller.ex        # GET /  → /static/index.html
│       │   └── ws_controller.ex          # GET /ws/:room_id  → upgrade
│       └── ws/chat_handler.ex            # WebSock behaviour
└── priv/
    ├── repo/migrations/                  # rooms / messages
    └── static/                           # index.html / app.js / style.css
```

## 실행

### 1. 의존성 설치

```bash
mix deps.get
```

### 2. SQLite (기본)

```bash
mix ecto.create
mix ecto.migrate
mix phx.server   # 또는 iex -S mix phx.server
```

`emoji_chat.db` 파일이 생성됩니다. http://localhost:8080 접속.

### 3. MySQL 사용 시

> ⚠️ Ecto Repo 는 **컴파일 타임**에 어댑터를 결정하므로, MySQL 로 전환하려면
> `DB_TYPE=mysql` 을 둔 채 다시 컴파일해야 합니다.

```bash
DB_TYPE=mysql mix deps.compile --force
DB_TYPE=mysql MYSQL_USER=root MYSQL_PASSWORD= mix ecto.create
DB_TYPE=mysql MYSQL_USER=root MYSQL_PASSWORD= mix ecto.migrate
DB_TYPE=mysql MYSQL_USER=root MYSQL_PASSWORD= mix phx.server
```

## 환경변수

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `PORT` | `8080` | HTTP 포트 (prod) |
| `DB_TYPE` | `sqlite` | `sqlite` 또는 `mysql` |
| `SQLITE_PATH` | `emoji_chat.db` | SQLite 파일 경로 |
| `MYSQL_HOST` | `127.0.0.1` | MySQL 호스트 |
| `MYSQL_PORT` | `3306` | MySQL 포트 |
| `MYSQL_DB` | `emoji_chat` | DB 이름 |
| `MYSQL_USER` | `root` | MySQL user |
| `MYSQL_PASSWORD` | (빈 값) | MySQL password |

## API

Go / Kotlin 버전과 동일합니다.

| Method | Path | 설명 |
|--------|------|------|
| `POST` | `/api/rooms` | 새 방 (`{name, owner_id}`) |
| `GET` | `/api/rooms/:id` | 방 조회 |
| `DELETE` | `/api/rooms/:id` | 방 삭제 (방장만, `X-User-Id` 헤더) |
| `GET` | `/api/rooms/:id/messages?limit=50&before=msgId` | 메시지 페이지네이션 |
| `GET` | `/ws/:room_id?userId=...` | raw WebSocket 연결 |

## 동시성 설계

- 방 하나당 **`EmojiChat.RoomPresence` GenServer 한 개** 가 떠 있고, 멤버 PID 를 모니터링합니다.
- 메시지 / 입퇴장 알림은 모두 `Phoenix.PubSub` 의 `"room:<id>"` 토픽으로 broadcast 합니다.
- 마지막 멤버가 떠나면 1시간 idle 후 자동으로 GenServer 가 정리됩니다 (Go 버전의 `roomIdleTimeout` 과 동일).
- WebSocket 프로세스 자체가 PubSub 구독자이며, 본인이 `Process.exit/2` 되면 monitor 가 트리거되어 `:DOWN` 으로 자동 leave 처리됩니다.

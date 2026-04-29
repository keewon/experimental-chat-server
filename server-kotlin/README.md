# 😜 Emoji Chat — Spring Boot (Kotlin)

Go 버전과 동일한 동작을 Kotlin + Spring Boot 로 재구현했습니다.

## 기술 스택

| 구분 | 기술 |
|------|------|
| Language | Kotlin 1.9, JVM 21 |
| Framework | Spring Boot 3.3, Spring Web, Spring WebSocket, Spring JDBC |
| Build | Gradle (Kotlin DSL) |
| DB Driver | `org.xerial:sqlite-jdbc`, `com.mysql:mysql-connector-j` |
| Frontend | Go 버전과 동일 (`src/main/resources/static/`) |

## 디렉토리

```
server-kotlin/
├── build.gradle.kts
├── settings.gradle.kts
├── schema.sql
├── src/main/kotlin/com/example/emojichat/
│   ├── EmojiChatApplication.kt   # 애플리케이션 + DataSource/Cors 설정
│   ├── Models.kt                 # Room / Message / WsMessage DTO
│   ├── EmojiValidator.kt         # 이모지 전용 검증 (Go의 isEmojiOnly 포팅)
│   ├── ChatRepository.kt         # JDBC + SQLite 자동 테이블 생성
│   ├── RoomController.kt         # /api/rooms 엔드포인트
│   ├── RoomHubManager.kt         # 방별 Hub, idle 시 자동 정리
│   ├── ChatWebSocketHandler.kt   # 메시지 처리 / 브로드캐스트
│   └── WebSocketConfig.kt        # /ws/{roomId} 핸드셰이크 (userId 쿼리 파싱)
└── src/main/resources/
    ├── application.yml
    └── static/                   # index.html / app.js / style.css
```

## 실행

### 1. 의존성 다운로드 & 실행 (SQLite, 기본)

```bash
./gradlew bootRun
```

루트에 `emoji_chat.db` 파일이 자동 생성되고 테이블이 만들어집니다.

### 2. MySQL 사용 시

```bash
mysql -u root < schema.sql
DB_TYPE=mysql MYSQL_USER=root MYSQL_PASSWORD= ./gradlew bootRun
```

JAR 빌드:

```bash
./gradlew bootJar
java -jar build/libs/emoji-chat-0.1.0.jar
```

## 환경변수

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `PORT` | `8080` | 서버 포트 |
| `DB_TYPE` | `sqlite` | `sqlite` 또는 `mysql` |
| `SQLITE_PATH` | `emoji_chat.db` | SQLite 파일 경로 |
| `MYSQL_DSN` | `jdbc:mysql://127.0.0.1:3306/emoji_chat?characterEncoding=utf8mb4&...` | JDBC URL |
| `MYSQL_USER` | `root` | MySQL user |
| `MYSQL_PASSWORD` | (빈 값) | MySQL password |

## API

Go 버전과 완전히 동일합니다.

| Method | Path | 설명 |
|--------|------|------|
| `POST` | `/api/rooms` | 새 방 생성 (`{name, owner_id}`) |
| `GET` | `/api/rooms/{id}` | 방 조회 |
| `DELETE` | `/api/rooms/{id}` | 방 삭제 (방장만, `X-User-Id` 헤더) |
| `GET` | `/api/rooms/{id}/messages?limit=50&before=msgId` | 메시지 페이지네이션 |
| `GET` | `/ws/{roomId}?userId=...` | WebSocket 연결 |

WebSocket JSON 메시지 포맷도 Go 버전과 동일하므로 `static/app.js`를 그대로 사용합니다.

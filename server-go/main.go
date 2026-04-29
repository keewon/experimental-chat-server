package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
)

// ─── DB Type ────────────────────────────────────────────────────

var dbType string // "mysql" or "sqlite"

// ─── Data Structures ────────────────────────────────────────────

type Room struct {
	ID        string `json:"id"`
	OwnerID   string `json:"owner_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type Message struct {
	ID        int64  `json:"id"`
	RoomID    string `json:"room_id"`
	UserID    string `json:"user_id"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type WSMessage struct {
	Type        string `json:"type"`
	Content     string `json:"content,omitempty"`
	ID          int64  `json:"id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	OnlineCount int    `json:"online_count,omitempty"`
	Message     string `json:"message,omitempty"`
}

type Client struct {
	roomHub     *RoomHub
	conn        *websocket.Conn
	userID      string
	send        chan []byte
	ip          string
	connectedAt time.Time
}

// RoomHub — 방 하나를 담당하는 독립 Hub
type RoomHub struct {
	roomID     string
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
	stop       chan struct{}
	manager    *HubManager
}

// HubManager — 모든 RoomHub를 관리 (생성, 조회, 정리)
type HubManager struct {
	hubs     map[string]*RoomHub
	mu       sync.Mutex
	idleTime time.Duration // 빈 방 정리까지 대기 시간
}

// ─── Globals ────────────────────────────────────────────────────

var (
	db       *sql.DB
	manager  *HubManager
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     checkOrigin,
	}
)

// checkOrigin validates the WebSocket handshake Origin header to prevent
// cross-site WebSocket hijacking. We allow:
//   - same-host (covers direct localhost access and cloudflared-forwarded
//     requests where Host header is preserved)
//   - PUBLIC_ORIGIN env var if explicitly set
// Empty Origin (non-browser clients) is rejected.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	host := r.Host
	if origin == "http://"+host || origin == "https://"+host {
		return true
	}
	if pub := os.Getenv("PUBLIC_ORIGIN"); pub != "" && origin == pub {
		return true
	}
	log.Printf("WS upgrade blocked: origin=%q host=%q", origin, host)
	return false
}

const roomIdleTimeout = 1 * time.Hour

// ─── HubManager ─────────────────────────────────────────────────

func newHubManager() *HubManager {
	return &HubManager{
		hubs:     make(map[string]*RoomHub),
		idleTime: roomIdleTimeout,
	}
}

// getOrCreateHub — 방 Hub를 가져오거나, 없으면 새로 만들어 시작
func (m *HubManager) getOrCreateHub(roomID string) *RoomHub {
	m.mu.Lock()
	defer m.mu.Unlock()

	if h, ok := m.hubs[roomID]; ok {
		return h
	}

	h := &RoomHub{
		roomID:     roomID,
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte, 256),
		stop:       make(chan struct{}),
		manager:    m,
	}
	m.hubs[roomID] = h
	go h.run()
	log.Printf("RoomHub started: %s", roomID)
	return h
}

// removeHub — HubManager에서 방 Hub 제거
func (m *HubManager) removeHub(roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.hubs, roomID)
	log.Printf("RoomHub removed: %s", roomID)
}

// ─── RoomHub ────────────────────────────────────────────────────

func (h *RoomHub) run() {
	var idleTimer *time.Timer

	// 시작 시 바로 idle 타이머 설정 (클라이언트 없이 생성될 수도 있으므로)
	idleTimer = time.NewTimer(h.manager.idleTime)

	defer func() {
		idleTimer.Stop()
		// 남은 클라이언트 모두 정리
		for client := range h.clients {
			close(client.send)
		}
		h.manager.removeHub(h.roomID)
	}()

	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			// 클라이언트가 들어왔으니 idle 타이머 중지
			idleTimer.Stop()

			count := len(h.clients)
			msg := WSMessage{
				Type:        "join",
				UserID:      client.userID,
				OnlineCount: count,
			}
			h.broadcastMsg(msg)

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}

			count := len(h.clients)
			msg := WSMessage{
				Type:        "leave",
				UserID:      client.userID,
				OnlineCount: count,
			}
			h.broadcastMsg(msg)

			// 마지막 클라이언트가 나갔으면 idle 타이머 시작
			if count == 0 {
				idleTimer.Reset(h.manager.idleTime)
			}

		case data := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- data:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}

		case <-idleTimer.C:
			// 타이머 만료 — 아직 비어있으면 종료
			if len(h.clients) == 0 {
				return
			}

		case <-h.stop:
			return
		}
	}
}

func (h *RoomHub) broadcastMsg(msg WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	for client := range h.clients {
		select {
		case client.send <- data:
		default:
			close(client.send)
			delete(h.clients, client)
		}
	}
}

func (h *RoomHub) clientCount() int {
	return len(h.clients)
}

// ─── WebSocket Client Pumps ─────────────────────────────────────

func (c *Client) readPump() {
	defer func() {
		c.roomHub.unregister <- c
		c.conn.Close()
		log.Printf("[ws disconnect] room=%s user=%s ip=%s dur=%s",
			c.roomHub.roomID, shortUserID(c.userID), c.ip,
			time.Since(c.connectedAt).Truncate(time.Second))
	}()
	c.conn.SetReadLimit(4096)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, rawMsg, err := c.conn.ReadMessage()
		if err != nil {
			break
		}

		var incoming WSMessage
		if err := json.Unmarshal(rawMsg, &incoming); err != nil {
			continue
		}

		if incoming.Type != "message" {
			continue
		}

		content := strings.TrimSpace(incoming.Content)
		if content == "" {
			continue
		}

		if !isEmojiOnly(content) {
			errMsg := WSMessage{Type: "error", Message: "emoji only! 🙅"}
			data, _ := json.Marshal(errMsg)
			c.send <- data
			continue
		}

		now := time.Now().UTC().Format(time.RFC3339)
		result, err := db.Exec(
			"INSERT INTO messages (room_id, user_id, content, created_at) VALUES (?, ?, ?, ?)",
			c.roomHub.roomID, c.userID, content, now,
		)
		if err != nil {
			log.Printf("DB insert error: %v", err)
			continue
		}

		msgID, _ := result.LastInsertId()
		broadcast := WSMessage{
			Type:      "message",
			ID:        msgID,
			UserID:    c.userID,
			Content:   content,
			CreatedAt: now,
		}
		data, _ := json.Marshal(broadcast)
		c.roomHub.broadcast <- data
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ─── Emoji Validation ───────────────────────────────────────────

func isEmojiOnly(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError {
			return false
		}

		// Allow zero-width joiner (for compound emoji like 👨‍👩‍👧)
		if r == 0x200D {
			i += size
			continue
		}
		// Allow variation selectors
		if r == 0xFE0E || r == 0xFE0F {
			i += size
			continue
		}
		// Allow skin tone modifiers
		if r >= 0x1F3FB && r <= 0x1F3FF {
			i += size
			continue
		}
		// Allow combining enclosing keycap
		if r == 0x20E3 {
			i += size
			continue
		}
		// Allow tag characters (used in flag sequences like 🏴󠁧󠁢)
		if r >= 0xE0020 && r <= 0xE007F {
			i += size
			continue
		}

		// Check if it's in recognized emoji ranges
		if isEmojiRune(r) {
			i += size
			continue
		}

		// Allow digits 0-9 and * # only if followed by variation selector + keycap
		if (r >= '0' && r <= '9') || r == '#' || r == '*' {
			i += size
			continue
		}

		return false
	}
	return true
}

func isEmojiRune(r rune) bool {
	// Common emoji ranges
	if r >= 0x1F600 && r <= 0x1F64F { return true } // Emoticons
	if r >= 0x1F300 && r <= 0x1F5FF { return true } // Misc Symbols & Pictographs
	if r >= 0x1F680 && r <= 0x1F6FF { return true } // Transport & Map
	if r >= 0x1F700 && r <= 0x1F77F { return true } // Alchemical Symbols
	if r >= 0x1F780 && r <= 0x1F7FF { return true } // Geometric Shapes Extended
	if r >= 0x1F800 && r <= 0x1F8FF { return true } // Supplemental Arrows-C
	if r >= 0x1F900 && r <= 0x1F9FF { return true } // Supplemental Symbols & Pictographs
	if r >= 0x1FA00 && r <= 0x1FA6F { return true } // Chess Symbols
	if r >= 0x1FA70 && r <= 0x1FAFF { return true } // Symbols & Pictographs Extended-A
	if r >= 0x2600 && r <= 0x26FF  { return true }  // Misc Symbols
	if r >= 0x2700 && r <= 0x27BF  { return true }  // Dingbats
	if r >= 0x2300 && r <= 0x23FF  { return true }  // Misc Technical
	if r >= 0x2B50 && r <= 0x2B55  { return true }  // Stars & circles
	if r >= 0x1F1E0 && r <= 0x1F1FF { return true } // Regional Indicator Symbols (flags)
	if r == 0x200D { return true }                    // ZWJ
	if r == 0x2764 { return true }                    // ❤
	if r == 0x2763 { return true }                    // ❣
	if r == 0x270D { return true }                    // ✍
	if r == 0x2744 { return true }                    // ❄
	if r >= 0xFE00 && r <= 0xFE0F { return true }   // Variation Selectors
	if r == 0x00A9 || r == 0x00AE { return true }   // © ®
	if r == 0x203C || r == 0x2049 { return true }   // ‼ ⁉
	if r >= 0x2100 && r <= 0x214F { return true }   // Letterlike Symbols
	if unicode.Is(unicode.So, r) { return true }     // Symbol, Other category
	return false
}

// ─── Dotenv ─────────────────────────────────────────────────────

// loadDotenv loads `.env` (and `.env.local` if present) into process env.
// Existing env vars are NOT overridden — values already in the environment
// take precedence, so production deployments can ignore the file entirely.
// Missing files are silently skipped; this is useful for local dev only.
func loadDotenv() {
	for _, name := range []string{".env", ".env.local"} {
		if _, err := os.Stat(name); err != nil {
			continue
		}
		if err := godotenv.Load(name); err != nil {
			log.Printf("warning: failed to load %s: %v", name, err)
		} else {
			log.Printf("loaded env from %s", name)
		}
	}
}

// ─── Session (HMAC-signed cookie) ───────────────────────────────

const (
	sessionCookieName = "emoji_chat_session"
	sessionMaxAge     = 365 * 24 * 3600 // 1 year
)

var sessionSecret []byte

func isTruthyEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func noCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the originating client's address, looking through the
// reverse proxy chain. Cloudflare sets CF-Connecting-IP to the true client.
// Falls back to the first hop in X-Forwarded-For, then RemoteAddr.
func clientIP(r *http.Request) string {
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return cf
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// shortUserID returns the first 8 hex chars of a UUID for log compactness.
func shortUserID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// statusRecorder wraps http.ResponseWriter so the access-log middleware
// can observe the status code and body size, while still allowing the
// gorilla/websocket upgrader to hijack the underlying connection.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (sr *statusRecorder) WriteHeader(status int) {
	if sr.status == 0 {
		sr.status = status
	}
	sr.ResponseWriter.WriteHeader(status)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	n, err := sr.ResponseWriter.Write(b)
	sr.bytes += n
	return n, err
}

func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := sr.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return h.Hijack()
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sr, r)

		userID := ""
		if uid, ok := getUserIDFromRequest(r); ok {
			userID = shortUserID(uid)
		}
		log.Printf("[http] %s %s %d %dB %s ip=%s user=%s",
			r.Method, r.URL.RequestURI(), sr.status, sr.bytes,
			time.Since(start).Truncate(time.Microsecond),
			clientIP(r), userID)
	})
}

func initSessionSecret() {
	s := os.Getenv("SESSION_SECRET")
	if s == "" {
		log.Fatal("SESSION_SECRET environment variable is required (use a long random string, e.g. `openssl rand -hex 32`)")
	}
	if len(s) < 32 {
		log.Fatal("SESSION_SECRET must be at least 32 characters")
	}
	sessionSecret = []byte(s)
}

func signCookie(userID string, exp time.Time) string {
	expStr := strconv.FormatInt(exp.Unix(), 10)
	payload := userID + "|" + expStr
	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "|" + sig
}

func verifyCookie(v string) (string, bool) {
	parts := strings.Split(v, "|")
	if len(parts) != 3 {
		return "", false
	}
	userID, expStr, sig := parts[0], parts[1], parts[2]
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", false
	}
	if time.Now().Unix() > exp {
		return "", false
	}
	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write([]byte(userID + "|" + expStr))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false
	}
	return userID, true
}

func getUserIDFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	return verifyCookie(c.Value)
}

// issueSession sets a fresh signed cookie on the response and returns the
// userId encoded inside it. The Secure flag follows the actual transport —
// true on direct HTTPS or when behind a proxy that sets X-Forwarded-Proto.
func issueSession(w http.ResponseWriter, r *http.Request) string {
	userID := uuid.New().String()
	exp := time.Now().Add(time.Duration(sessionMaxAge) * time.Second)
	value := signCookie(userID, exp)

	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sessionMaxAge,
		Path:     "/",
	})
	return userID
}

// requireUserID enforces a valid session for protected endpoints.
func requireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID, ok := getUserIDFromRequest(r)
	if !ok {
		jsonError(w, "session required", http.StatusUnauthorized)
		return "", false
	}
	return userID, true
}

func handleSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromRequest(r)
	if !ok {
		userID = issueSession(w, r)
	}
	jsonResponse(w, map[string]string{"user_id": userID}, http.StatusOK)
}

// ─── HTTP Handlers ──────────────────────────────────────────────

func handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	roomID := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		"INSERT INTO rooms (id, owner_id, name, created_at) VALUES (?, ?, ?, ?)",
		roomID, userID, req.Name, now,
	)
	if err != nil {
		log.Printf("DB error creating room: %v", err)
		jsonError(w, "failed to create room", http.StatusInternalServerError)
		return
	}

	room := Room{ID: roomID, OwnerID: userID, Name: req.Name, CreatedAt: now}
	jsonResponse(w, room, http.StatusCreated)
}

func handleGetRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if roomID == "" {
		jsonError(w, "room id required", http.StatusBadRequest)
		return
	}

	var room Room
	err := db.QueryRow(
		"SELECT id, owner_id, name, created_at FROM rooms WHERE id = ?", roomID,
	).Scan(&room.ID, &room.OwnerID, &room.Name, &room.CreatedAt)
	if err == sql.ErrNoRows {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, room, http.StatusOK)
}

func handleDeleteRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")

	var ownerID string
	err := db.QueryRow("SELECT owner_id FROM rooms WHERE id = ?", roomID).Scan(&ownerID)
	if err == sql.ErrNoRows {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if ownerID != userID {
		jsonError(w, "only the room owner can delete", http.StatusForbidden)
		return
	}

	db.Exec("DELETE FROM rooms WHERE id = ?", roomID)
	w.WriteHeader(http.StatusNoContent)
}

func handleGetMessages(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	limitStr := r.URL.Query().Get("limit")
	beforeStr := r.URL.Query().Get("before")

	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}

	var rows *sql.Rows
	var err error
	if beforeStr != "" {
		before, _ := strconv.ParseInt(beforeStr, 10, 64)
		rows, err = db.Query(
			"SELECT id, room_id, user_id, content, created_at FROM messages WHERE room_id = ? AND id < ? ORDER BY id DESC LIMIT ?",
			roomID, before, limit,
		)
	} else {
		rows, err = db.Query(
			"SELECT id, room_id, user_id, content, created_at FROM messages WHERE room_id = ? ORDER BY id DESC LIMIT ?",
			roomID, limit,
		)
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	messages := make([]Message, 0)
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.RoomID, &m.UserID, &m.Content, &m.CreatedAt); err != nil {
			continue
		}
		messages = append(messages, m)
	}

	// Reverse to chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	jsonResponse(w, map[string]any{"messages": messages}, http.StatusOK)
}

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, map[string]string{
		"public_origin": os.Getenv("PUBLIC_ORIGIN"),
	}, http.StatusOK)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	userID, ok := getUserIDFromRequest(r)
	if !ok {
		http.Error(w, "session required", http.StatusUnauthorized)
		return
	}
	roomID := r.PathValue("roomId")
	if roomID == "" {
		http.Error(w, "missing roomId", http.StatusBadRequest)
		return
	}

	// Verify room exists
	var count int
	db.QueryRow("SELECT COUNT(*) FROM rooms WHERE id = ?", roomID).Scan(&count)
	if count == 0 {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	roomHub := manager.getOrCreateHub(roomID)
	client := &Client{
		roomHub:     roomHub,
		conn:        conn,
		userID:      userID,
		send:        make(chan []byte, 256),
		ip:          clientIP(r),
		connectedAt: time.Now(),
	}
	roomHub.register <- client
	log.Printf("[ws connect] room=%s user=%s ip=%s", roomID, shortUserID(userID), client.ip)

	go client.writePump()
	go client.readPump()
}

// ─── Helpers ────────────────────────────────────────────────────

func jsonResponse(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, message string, status int) {
	jsonResponse(w, map[string]string{"error": message}, status)
}

// ─── Database Init ──────────────────────────────────────────────

func initDB() {
	dbType = os.Getenv("DB_TYPE")
	if dbType == "" {
		dbType = "sqlite" // default to sqlite for local dev
	}

	var err error
	switch dbType {
	case "mysql":
		dsn := os.Getenv("MYSQL_DSN")
		if dsn == "" {
			dsn = "root:@tcp(127.0.0.1:3306)/emoji_chat?charset=utf8mb4&parseTime=true&loc=UTC"
		}
		db, err = sql.Open("mysql", dsn)
		if err != nil {
			log.Fatalf("Failed to connect to MySQL: %v", err)
		}
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(5 * time.Minute)
		if err := db.Ping(); err != nil {
			log.Fatalf("Failed to ping MySQL: %v", err)
		}
		log.Println("Connected to MySQL")

	case "sqlite":
		dsn := os.Getenv("SQLITE_PATH")
		if dsn == "" {
			dsn = "emoji_chat.db"
		}
		db, err = sql.Open("sqlite3", dsn)
		if err != nil {
			log.Fatalf("Failed to open SQLite: %v", err)
		}
		db.SetMaxOpenConns(1) // SQLite supports one writer at a time
		// Enable WAL mode for better concurrent read performance
		db.Exec("PRAGMA journal_mode=WAL")
		db.Exec("PRAGMA foreign_keys=ON")

		// Auto-create tables
		if err := createSQLiteTables(); err != nil {
			log.Fatalf("Failed to create SQLite tables: %v", err)
		}
		log.Printf("Connected to SQLite (%s)", dsn)

	default:
		log.Fatalf("Unsupported DB_TYPE: %s (use 'mysql' or 'sqlite')", dbType)
	}
}

func createSQLiteTables() error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS rooms (
			id         TEXT NOT NULL PRIMARY KEY,
			owner_id   TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		return fmt.Errorf("create rooms table: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			room_id    TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			content    TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return fmt.Errorf("create messages table: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_room_created ON messages(room_id, created_at)`)
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}

	return nil
}

// ─── Main ───────────────────────────────────────────────────────

func main() {
	loadDotenv()
	initSessionSecret()
	initDB()

	manager = newHubManager()

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("POST /api/rooms", handleCreateRoom)
	mux.HandleFunc("GET /api/rooms/{id}", handleGetRoom)
	mux.HandleFunc("DELETE /api/rooms/{id}", handleDeleteRoom)
	mux.HandleFunc("GET /api/rooms/{id}/messages", handleGetMessages)
	mux.HandleFunc("GET /api/config", handleGetConfig)
	mux.HandleFunc("GET /api/session", handleSession)
	mux.HandleFunc("GET /ws/{roomId}", handleWebSocket)

	// Static files
	fs := http.FileServer(http.Dir("static"))
	staticHandler := http.StripPrefix("/static/", fs)
	if isTruthyEnv("STATIC_NO_CACHE") {
		staticHandler = noCacheMiddleware(staticHandler)
	}
	mux.Handle("GET /static/", staticHandler)

	// Root redirect
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/static/index.html", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	// Default to loopback so the port is not exposed on public interfaces.
	// Set BIND_ADDR=0.0.0.0 to listen on all interfaces.
	bindAddr := os.Getenv("BIND_ADDR")
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	listenAddr := bindAddr + ":" + port

	fmt.Printf("🎉 Emoji Chat server running on http://%s\n", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, accessLog(mux)))
}

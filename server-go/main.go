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

const (
	RoomKindGroup = "group"
	RoomKindLobby = "lobby"

	RoomVisibilityPublic  = "public"
	RoomVisibilityPrivate = "private"

	RoleOwner  = "owner"
	RoleMember = "member"

	LobbyRoomID = "lobby"

	GroupMemberCap = 100
)

type User struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

type Room struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`       // 'group' | 'lobby'
	Visibility string  `json:"visibility"` // 'public' | 'private'
	Name       string  `json:"name"`
	EmojiOnly  bool    `json:"emoji_only"`
	OwnerID    *string `json:"owner_id,omitempty"` // null for lobby
	CreatedAt  string  `json:"created_at"`
}

type Member struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	JoinedAt    string `json:"joined_at"`
}

type Message struct {
	ID          int64  `json:"id"`
	RoomID      string `json:"room_id"`
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name,omitempty"`
	Content     string `json:"content"`
	CreatedAt   string `json:"created_at"`
}

type WSMessage struct {
	Type        string `json:"type"`
	RoomID      string `json:"room_id,omitempty"`
	Content     string `json:"content,omitempty"`
	ID          int64  `json:"id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
	OnlineCount int    `json:"online_count,omitempty"` // used by single-room hub broadcasts; revisit in step 3
}

type Client struct {
	conn        *websocket.Conn
	userID      string
	displayName string

	send chan []byte

	hubs   map[string]*RoomHub
	hubsMu sync.Mutex

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
	// done is closed by run() right before it exits. External senders
	// (register/unregister/broadcast) select on it to avoid blocking on
	// a hub whose run loop has already returned.
	done    chan struct{}
	manager *HubManager
}

// HubManager — 모든 RoomHub를 관리 (생성, 조회, 정리)
//
// hubs is a sync.Map keyed by roomID → *RoomHub. We chose sync.Map over a
// plain map+mutex because: (1) the workload is read-heavy (each WS connect
// is a Load, each new room is a single Store), (2) keys are disjoint per
// goroutine, and (3) it lets a hub atomically detach itself on idle exit
// via CompareAndDelete, closing the lookup-vs-teardown race.
type HubManager struct {
	hubs     sync.Map
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
		idleTime: roomIdleTimeout,
	}
}

func newRoomHub(roomID string, m *HubManager) *RoomHub {
	return &RoomHub{
		roomID:     roomID,
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte, 256),
		done:       make(chan struct{}),
		manager:    m,
	}
}

// attachClient looks up (or creates) the hub for roomID and registers the
// client with it. Returns the hub the client ended up in. If a freshly
// found hub races with its own teardown, we transparently retry against a
// new hub — callers never see a "dying" hub.
func (m *HubManager) attachClient(roomID string, c *Client) *RoomHub {
	for {
		// Fast path — hub already exists.
		if v, ok := m.hubs.Load(roomID); ok {
			h := v.(*RoomHub)
			if h.tryRegister(c) {
				return h
			}
			// Hub closed its done channel between our Load and our send.
			// Loop and create / find a fresh one.
			continue
		}

		// Slow path — try to install a fresh hub.
		h := newRoomHub(roomID, m)
		actual, loaded := m.hubs.LoadOrStore(roomID, h)
		if loaded {
			// Lost the create race; another goroutine installed first.
			existing := actual.(*RoomHub)
			if existing.tryRegister(c) {
				return existing
			}
			continue
		}

		// We own this hub now — start it and register.
		go h.run()
		log.Printf("RoomHub started: %s", roomID)
		if h.tryRegister(c) {
			return h
		}
		// Extremely unlikely: hub torn down before we could register.
		// (Idle timer is 1h, so this won't happen in practice.)
	}
}

// ─── RoomHub ────────────────────────────────────────────────────

// tryRegister synchronously hands the client to the hub's run loop.
// Returns false if the hub has already shut down — caller should retry
// against a fresh hub via attachClient. Both branches of the select are
// safe even if run() is mid-exit: once it closes done, this select fires
// the done case instead of a register send that would have nobody to
// receive it.
func (h *RoomHub) tryRegister(c *Client) bool {
	select {
	case h.register <- c:
		return true
	case <-h.done:
		return false
	}
}

func (h *RoomHub) run() {
	// Lobby is permanent — never let its idle timer fire teardown.
	idleTimer := time.NewTimer(h.manager.idleTime)
	if h.roomID == LobbyRoomID {
		idleTimer.Stop()
	}
	defer idleTimer.Stop()

	for {
		select {
		case client := <-h.register:
			// Multi-room model: connection-level join is no longer broadcast;
			// the REST member_join event is what membership-aware UIs care about.
			h.clients[client] = true
			if h.roomID != LobbyRoomID {
				idleTimer.Stop()
			}

		case client := <-h.unregister:
			// We do NOT close client.send here — that channel is shared
			// across all the hubs this client is in, and is owned by the
			// connection's readPump (which closes it exactly once on exit).
			delete(h.clients, client)
			if len(h.clients) == 0 && h.roomID != LobbyRoomID {
				idleTimer.Reset(h.manager.idleTime)
			}

		case data := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- data:
				default:
					// Slow client; drop from this hub's set and close the
					// underlying connection so readPump tears everything down.
					delete(h.clients, client)
					if client.conn != nil {
						client.conn.Close()
					}
				}
			}

		case <-idleTimer.C:
			if h.roomID == LobbyRoomID {
				continue
			}
			if len(h.clients) == 0 {
				if h.manager.hubs.CompareAndDelete(h.roomID, h) {
					close(h.done)
					log.Printf("RoomHub removed: %s", h.roomID)
					return
				}
				idleTimer.Reset(h.manager.idleTime)
			}
		}
	}
}

// ─── WebSocket Client Pumps ─────────────────────────────────────

func (c *Client) readPump() {
	defer func() {
		// Snapshot all hubs we're attached to (under lock), then unregister
		// from each. send is owned by this goroutine — close it exactly
		// once here so writePump exits cleanly.
		c.hubsMu.Lock()
		hubs := make([]*RoomHub, 0, len(c.hubs))
		for _, h := range c.hubs {
			hubs = append(hubs, h)
		}
		c.hubs = nil
		c.hubsMu.Unlock()

		for _, h := range hubs {
			select {
			case h.unregister <- c:
			case <-h.done:
			}
		}
		clientRegistry.remove(c)
		close(c.send)
		c.conn.Close()
		log.Printf("[ws disconnect] user=%s ip=%s dur=%s",
			shortUserID(c.userID), c.ip,
			time.Since(c.connectedAt).Truncate(time.Second))
	}()

	c.conn.SetReadLimit(8192)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var inc wsIncoming
		if err := json.Unmarshal(raw, &inc); err != nil {
			continue
		}
		if inc.Type != WSTypeMsg {
			continue
		}
		c.handleIncomingMsg(inc)
	}
}

const messageMaxRunes = 1000

func (c *Client) handleIncomingMsg(inc wsIncoming) {
	content := strings.TrimSpace(inc.Content)
	if content == "" || inc.RoomID == "" {
		return
	}
	if utf8.RuneCountInString(content) > messageMaxRunes {
		c.sendError("too_long", "메시지가 너무 길어요")
		return
	}

	_, _, isMember, err := roomMembership(inc.RoomID, c.userID)
	if err != nil || !isMember {
		c.sendError("not_member", "이 방의 멤버가 아닙니다")
		return
	}

	room, err := getRoom(inc.RoomID)
	if err != nil {
		return
	}
	if room.EmojiOnly && !isEmojiOnly(content) {
		c.sendError("emoji_only", "이 방은 이모지만 보낼 수 있어요 🙅")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(
		"INSERT INTO messages (room_id, user_id, content, created_at) VALUES (?, ?, ?, ?)",
		inc.RoomID, c.userID, content, now,
	)
	if err != nil {
		log.Printf("DB insert error: %v", err)
		return
	}
	msgID, _ := res.LastInsertId()

	c.hubsMu.Lock()
	displayName := c.displayName
	h := c.hubs[inc.RoomID]
	c.hubsMu.Unlock()
	if h == nil {
		return
	}

	evt := wsMsgEvent{
		Type:        WSTypeMsg,
		RoomID:      inc.RoomID,
		ID:          msgID,
		UserID:      c.userID,
		DisplayName: displayName,
		Content:     content,
		CreatedAt:   now,
	}
	data, _ := json.Marshal(evt)

	select {
	case h.broadcast <- data:
	case <-h.done:
	}
}

func (c *Client) sendError(code, msg string) {
	data, _ := json.Marshal(wsErrorEvent{Type: WSTypeError, Code: code, Message: msg})
	select {
	case c.send <- data:
	default:
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

// NOTE: Room CRUD handlers (create/get/delete/leave/transfer/etc.) are
// implemented in step 2 of the messenger redesign. The old single-room
// emoji-chat REST surface has been removed deliberately; the new endpoints
// will replace it.

func handleGetMessages(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	var before int64
	if b, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil {
		before = b
	}
	messages, err := listMessages(roomID, before, limit)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
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
	user, err := ensureUser(userID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if user.DisplayName == "" {
		http.Error(w, "display_name required", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &Client{
		conn:        conn,
		userID:      userID,
		displayName: user.DisplayName,
		send:        make(chan []byte, 256),
		hubs:        make(map[string]*RoomHub),
		ip:          clientIP(r),
		connectedAt: time.Now(),
	}
	clientRegistry.add(client)

	rooms, err := myRoomsForSnapshot(userID)
	if err != nil {
		log.Printf("snapshot build: %v", err)
	}

	// Push the snapshot first so clients always see "what rooms am I in"
	// before any live message lands. The send buffer (256) is large enough
	// that this push cannot block on a fresh connection.
	snap := wsSnapshot{Type: WSTypeSnapshot, Me: user, Rooms: rooms}
	if data, err := json.Marshal(snap); err == nil {
		client.send <- data
	}

	// Attach to every room the user is a member of (lobby included). After
	// this point, live broadcasts can flow into client.send.
	for _, e := range rooms {
		h := manager.attachClient(e.Room.ID, client)
		client.hubsMu.Lock()
		client.hubs[e.Room.ID] = h
		client.hubsMu.Unlock()
	}

	log.Printf("[ws connect] user=%s ip=%s rooms=%d",
		shortUserID(userID), client.ip, len(rooms))

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
		if err := seedLobby(); err != nil {
			log.Fatalf("Failed to seed lobby: %v", err)
		}
		log.Printf("Connected to SQLite (%s)", dsn)

	default:
		log.Fatalf("Unsupported DB_TYPE: %s (use 'mysql' or 'sqlite')", dbType)
	}
}

func createSQLiteTables() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id           TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			created_at   TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS rooms (
			id           TEXT PRIMARY KEY,
			kind         TEXT NOT NULL,
			visibility   TEXT NOT NULL,
			name         TEXT NOT NULL DEFAULT '',
			emoji_only   INTEGER NOT NULL DEFAULT 0,
			owner_id     TEXT,
			created_at   TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS room_members (
			room_id    TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			role       TEXT NOT NULL DEFAULT 'member',
			joined_at  TEXT NOT NULL,
			hidden     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (room_id, user_id),
			FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			room_id    TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			content    TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS invites (
			token      TEXT PRIMARY KEY,
			room_id    TEXT NOT NULL,
			created_by TEXT NOT NULL,
			created_at TEXT NOT NULL,
			revoked_at TEXT,
			FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_room_id ON messages(room_id, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_room_members_user ON room_members(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_rooms_kind_visibility ON rooms(kind, visibility)`,
	}

	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("schema: %w (stmt: %s)", err, firstLine(s))
		}
	}
	return nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// seedLobby ensures the singleton lobby room exists. Idempotent — safe to
// call on every start. Membership is implicit (every user is a member),
// so we do not seed room_members here.
func seedLobby() error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO rooms (id, kind, visibility, name, emoji_only, owner_id, created_at)
		SELECT ?, ?, ?, ?, 0, NULL, ?
		WHERE NOT EXISTS (SELECT 1 FROM rooms WHERE id = ?)
	`, LobbyRoomID, RoomKindLobby, RoomVisibilityPublic, "🏛️ 로비", now, LobbyRoomID)
	if err != nil {
		return fmt.Errorf("seed lobby: %w", err)
	}
	return nil
}

// ─── Main ───────────────────────────────────────────────────────

// buildMux assembles the router. Extracted from main so tests can wire up
// the handlers without binding a real port.
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Session / config
	mux.HandleFunc("GET /api/session", handleSession)
	mux.HandleFunc("GET /api/config", handleGetConfig)

	// Profile
	mux.HandleFunc("GET /api/me", handleGetMe)
	mux.HandleFunc("PUT /api/me", handlePutMe)

	// Rooms — note: literal /api/rooms/public is more specific than the
	// {id} pattern so Go's mux routes correctly without collision.
	mux.HandleFunc("GET /api/rooms/public", handleGetPublicRooms)
	mux.HandleFunc("POST /api/rooms", handleCreateRoom)
	mux.HandleFunc("GET /api/rooms/{id}", handleGetRoom)
	mux.HandleFunc("PATCH /api/rooms/{id}", handlePatchRoom)
	mux.HandleFunc("POST /api/rooms/{id}/transfer", handleTransferOwner)
	mux.HandleFunc("POST /api/rooms/{id}/leave", handleLeaveRoom)
	mux.HandleFunc("POST /api/rooms/{id}/hide", handleHideRoom)
	mux.HandleFunc("POST /api/rooms/{id}/unhide", handleUnhideRoom)
	mux.HandleFunc("POST /api/rooms/{id}/join", handleJoinRoom)
	mux.HandleFunc("POST /api/rooms/{id}/invite", handleCreateInvite)
	mux.HandleFunc("GET /api/rooms/{id}/members", handleGetMembers)
	mux.HandleFunc("GET /api/rooms/{id}/messages", handleGetMessages)

	// Invite preview
	mux.HandleFunc("GET /api/invites/{token}", handleGetInvite)

	// WebSocket — single multiplexed connection per user.
	mux.HandleFunc("GET /ws", handleWebSocket)

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

	return mux
}

func main() {
	loadDotenv()
	initSessionSecret()
	initDB()

	manager = newHubManager()
	mux := buildMux()

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

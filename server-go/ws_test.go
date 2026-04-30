package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startTestServer wires up the full mux backed by an in-memory DB and a
// fresh hub manager, then returns an httptest.Server. Cleanup is registered
// on the test.
func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	setupTestDB(t)
	setupSessionSecret(t)
	setupManager(t, 200*time.Millisecond) // short idle so teardown is observable

	srv := httptest.NewServer(buildMux())
	t.Cleanup(srv.Close)
	return srv
}

// httpClientWithCookies returns a client that persists cookies across
// requests — same behavior the browser has on same-origin fetches.
func httpClientWithCookies(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("new cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

// fetchSession performs the GET /api/session handshake against srv,
// populates the client's jar with the session cookie, and returns the
// user_id the server just issued.
func fetchSession(t *testing.T, srv *httptest.Server, c *http.Client) string {
	t.Helper()
	resp, err := c.Get(srv.URL + "/api/session")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		UserID string `json:"user_id"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.UserID == "" {
		t.Fatal("session response missing user_id")
	}
	return body.UserID
}

func createRoom(t *testing.T, srv *httptest.Server, c *http.Client) string {
	t.Helper()
	resp, err := c.Post(srv.URL+"/api/rooms", "application/json",
		bytes.NewBufferString(`{"name":"💬"}`))
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room status=%d", resp.StatusCode)
	}
	var room Room
	json.NewDecoder(resp.Body).Decode(&room)
	return room.ID
}

// dialWS opens a WebSocket connection using cookies from c's jar so the
// session-cookie auth check passes. Origin is set to the server's URL so
// checkOrigin allows the upgrade.
func dialWS(t *testing.T, srv *httptest.Server, c *http.Client, roomID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	wsURL := "ws://" + u.Host + "/ws/" + roomID

	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Origin", srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		header.Add("Cookie", ck.Name+"="+ck.Value)
	}
	return dialer.Dial(wsURL, header)
}

func TestWS_RequiresSessionCookie(t *testing.T) {
	srv := startTestServer(t)

	u, _ := url.Parse(srv.URL)
	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Origin", srv.URL)

	_, resp, err := dialer.Dial("ws://"+u.Host+"/ws/whatever", header)
	if err == nil {
		t.Fatal("expected dial to fail without session cookie")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%v, want 401", resp)
	}
}

func TestWS_RejectsBadOrigin(t *testing.T) {
	srv := startTestServer(t)
	c := httpClientWithCookies(t)
	fetchSession(t, srv, c)
	roomID := createRoom(t, srv, c)

	u, _ := url.Parse(srv.URL)
	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Origin", "https://attacker.example.com")
	for _, ck := range c.Jar.Cookies(u) {
		header.Add("Cookie", ck.Name+"="+ck.Value)
	}

	_, resp, err := dialer.Dial("ws://"+u.Host+"/ws/"+roomID, header)
	if err == nil {
		t.Fatal("dial should have been rejected by checkOrigin")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%v, want 403", resp)
	}
}

func TestWS_RoomMustExist(t *testing.T) {
	srv := startTestServer(t)
	c := httpClientWithCookies(t)
	fetchSession(t, srv, c)

	_, resp, err := dialWS(t, srv, c, "ghost-room")
	if err == nil {
		t.Fatal("dial should have failed for missing room")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%v, want 404", resp)
	}
}

func TestWS_BroadcastBetweenClients(t *testing.T) {
	srv := startTestServer(t)

	cA := httpClientWithCookies(t)
	cB := httpClientWithCookies(t)
	fetchSession(t, srv, cA)
	fetchSession(t, srv, cB)

	roomID := createRoom(t, srv, cA)

	connA, _, err := dialWS(t, srv, cA, roomID)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer connA.Close()

	if got := readJoinOrLeave(t, connA, "join"); got.OnlineCount != 1 {
		t.Fatalf("A online_count after self-join=%d, want 1", got.OnlineCount)
	}

	connB, _, err := dialWS(t, srv, cB, roomID)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer connB.Close()

	if got := readJoinOrLeave(t, connA, "join"); got.OnlineCount != 2 {
		t.Fatalf("A sees B-join online_count=%d, want 2", got.OnlineCount)
	}
	if got := readJoinOrLeave(t, connB, "join"); got.OnlineCount != 2 {
		t.Fatalf("B sees self-join online_count=%d, want 2", got.OnlineCount)
	}

	// A sends an emoji; both should receive it.
	mustWriteJSON(t, connA, WSMessage{Type: "message", Content: "🚀"})
	for i, conn := range []*websocket.Conn{connA, connB} {
		msg := readNextMessage(t, conn)
		if msg.Type != "message" || msg.Content != "🚀" {
			t.Fatalf("conn %d got %+v, want message=🚀", i, msg)
		}
	}

	// Non-emoji from B → only B receives an error reply, no broadcast.
	mustWriteJSON(t, connB, WSMessage{Type: "message", Content: "hello world"})
	errMsg := readNextMessage(t, connB)
	if errMsg.Type != "error" {
		t.Fatalf("expected error reply for non-emoji, got %+v", errMsg)
	}

	// Closing B should leave A with online_count=1.
	connB.Close()
	if got := readJoinOrLeave(t, connA, "leave"); got.OnlineCount != 1 {
		t.Fatalf("A sees B-leave online_count=%d, want 1", got.OnlineCount)
	}
}

// ─── helpers ────────────────────────────────────────────────────

func readNextMessage(t *testing.T, conn *websocket.Conn) WSMessage {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg WSMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read: %v", err)
	}
	return msg
}

// readJoinOrLeave reads messages until one of the requested type appears,
// or until the deadline. Skips over interleaved "message" frames so the
// caller can assert specifically on join/leave.
func readJoinOrLeave(t *testing.T, conn *websocket.Conn, want string) WSMessage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var msg WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read while waiting for %s: %v", want, err)
		}
		if msg.Type == want {
			return msg
		}
	}
	t.Fatalf("did not receive %s within deadline", want)
	return WSMessage{}
}

func mustWriteJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("write: %v", err)
	}
}

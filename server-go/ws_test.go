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

func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	setupTestDB(t)
	setupSessionSecret(t)
	setupManager(t, 200*time.Millisecond)
	if err := seedLobby(); err != nil {
		t.Fatalf("seed lobby: %v", err)
	}
	srv := httptest.NewServer(buildMux())
	t.Cleanup(srv.Close)
	return srv
}

func httpClientWithCookies(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("new cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

// bootstrap performs /api/session and PUT /api/me so the test client is
// fully authenticated and has a non-empty display_name (the WS handler
// requires both).
func bootstrap(t *testing.T, srv *httptest.Server, c *http.Client, displayName string) string {
	t.Helper()
	resp, err := c.Get(srv.URL + "/api/session")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer resp.Body.Close()
	var sb struct {
		UserID string `json:"user_id"`
	}
	json.NewDecoder(resp.Body).Decode(&sb)

	body, _ := json.Marshal(map[string]string{"display_name": displayName})
	req, _ := http.NewRequest("PUT", srv.URL+"/api/me", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp2, err := c.Do(req)
	if err != nil {
		t.Fatalf("put me: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("put me status=%d", resp2.StatusCode)
	}
	return sb.UserID
}

func dialWS(t *testing.T, srv *httptest.Server, c *http.Client) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Origin", srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		header.Add("Cookie", ck.Name+"="+ck.Value)
	}
	return dialer.Dial("ws://"+u.Host+"/ws", header)
}

func TestWS_RequiresSessionCookie(t *testing.T) {
	srv := startTestServer(t)

	u, _ := url.Parse(srv.URL)
	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Origin", srv.URL)

	_, resp, err := dialer.Dial("ws://"+u.Host+"/ws", header)
	if err == nil {
		t.Fatal("expected dial to fail without session cookie")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%v, want 401", resp)
	}
}

func TestWS_RequiresDisplayName(t *testing.T) {
	srv := startTestServer(t)
	c := httpClientWithCookies(t)
	// Get a session cookie but skip PUT /api/me — display_name remains "".
	c.Get(srv.URL + "/api/session")

	_, resp, err := dialWS(t, srv, c)
	if err == nil {
		t.Fatal("expected dial to fail without display_name")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%v, want 403", resp)
	}
}

func TestWS_SnapshotIncludesLobby(t *testing.T) {
	srv := startTestServer(t)
	c := httpClientWithCookies(t)
	bootstrap(t, srv, c, "Alice")

	conn, _, err := dialWS(t, srv, c)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	snap := readSnapshot(t, conn)
	if snap.Me.DisplayName != "Alice" {
		t.Fatalf("me.display_name=%q", snap.Me.DisplayName)
	}
	hasLobby := false
	for _, e := range snap.Rooms {
		if e.Room.ID == LobbyRoomID {
			hasLobby = true
			if e.MyRole != RoleMember {
				t.Errorf("lobby my_role=%q", e.MyRole)
			}
		}
	}
	if !hasLobby {
		t.Fatalf("snapshot missing lobby")
	}
}

func TestWS_BroadcastsAcrossRooms(t *testing.T) {
	srv := startTestServer(t)

	cA := httpClientWithCookies(t)
	cB := httpClientWithCookies(t)
	bootstrap(t, srv, cA, "Alice")
	bootstrap(t, srv, cB, "Bob")

	connA, _, err := dialWS(t, srv, cA)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer connA.Close()
	connB, _, err := dialWS(t, srv, cB)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer connB.Close()

	readSnapshot(t, connA)
	readSnapshot(t, connB)

	// Send a lobby message from A; B (also in lobby implicitly) must receive it.
	mustWriteJSON(t, connA, wsIncoming{Type: WSTypeMsg, RoomID: LobbyRoomID, Content: "hi 🚀"})

	for i, conn := range []*websocket.Conn{connA, connB} {
		evt := readEvent(t, conn, WSTypeMsg)
		if evt["content"] != "hi 🚀" {
			t.Fatalf("conn %d content=%v", i, evt["content"])
		}
		if evt["display_name"] != "Alice" {
			t.Fatalf("conn %d display_name=%v", i, evt["display_name"])
		}
	}
}

func TestWS_MemberJoinDeliversLive(t *testing.T) {
	srv := startTestServer(t)

	cOwner := httpClientWithCookies(t)
	cJoiner := httpClientWithCookies(t)
	bootstrap(t, srv, cOwner, "Owner")
	bootstrap(t, srv, cJoiner, "Joiner")

	// Owner creates a public group room.
	roomID := createPublicRoom(t, srv, cOwner)

	connOwner, _, _ := dialWS(t, srv, cOwner)
	defer connOwner.Close()
	connJoiner, _, _ := dialWS(t, srv, cJoiner)
	defer connJoiner.Close()
	readSnapshot(t, connOwner)
	readSnapshot(t, connJoiner)

	// Joiner joins the room via REST.
	resp, err := cJoiner.Post(srv.URL+"/api/rooms/"+roomID+"/join", "application/json", nil)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	resp.Body.Close()

	// Owner must observe member_join broadcast on the room hub.
	evt := readEvent(t, connOwner, WSTypeMemberJoin)
	if evt["room_id"] != roomID {
		t.Fatalf("member_join room_id=%v", evt["room_id"])
	}

	// Joiner's connection must observe room_created so the new room
	// shows up in the sidebar without a reload.
	rce := readEvent(t, connJoiner, WSTypeRoomCreated)
	room, _ := rce["room"].(map[string]any)
	if room == nil || room["id"] != roomID {
		t.Fatalf("room_created payload=%v", rce)
	}
}

func TestWS_NameChangeReachesPeers(t *testing.T) {
	srv := startTestServer(t)
	cA := httpClientWithCookies(t)
	cB := httpClientWithCookies(t)
	bootstrap(t, srv, cA, "Alice")
	bootstrap(t, srv, cB, "Bob")

	connA, _, _ := dialWS(t, srv, cA)
	defer connA.Close()
	connB, _, _ := dialWS(t, srv, cB)
	defer connB.Close()
	readSnapshot(t, connA)
	readSnapshot(t, connB)

	// A renames; B (sharing lobby with A) should receive name_changed.
	body, _ := json.Marshal(map[string]string{"display_name": "Alice🦊"})
	req, _ := http.NewRequest("PUT", srv.URL+"/api/me", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := cA.Do(req)
	if err != nil {
		t.Fatalf("put me: %v", err)
	}
	resp.Body.Close()

	evt := readEvent(t, connB, WSTypeNameChanged)
	if evt["display_name"] != "Alice🦊" {
		t.Fatalf("name_changed payload=%v", evt)
	}
}

func TestWS_EmojiOnlyEnforced(t *testing.T) {
	srv := startTestServer(t)
	cOwner := httpClientWithCookies(t)
	bootstrap(t, srv, cOwner, "Owner")

	body, _ := json.Marshal(map[string]any{
		"visibility": "public", "name": "emojis", "emoji_only": true,
	})
	req, _ := http.NewRequest("POST", srv.URL+"/api/rooms", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := cOwner.Do(req)
	defer resp.Body.Close()
	var room Room
	json.NewDecoder(resp.Body).Decode(&room)

	conn, _, _ := dialWS(t, srv, cOwner)
	defer conn.Close()
	readSnapshot(t, conn)

	mustWriteJSON(t, conn, wsIncoming{Type: WSTypeMsg, RoomID: room.ID, Content: "hello"})

	evt := readEvent(t, conn, WSTypeError)
	if evt["code"] != "emoji_only" {
		t.Fatalf("error code=%v", evt["code"])
	}
}

// ─── helpers ────────────────────────────────────────────────────

func readSnapshot(t *testing.T, conn *websocket.Conn) wsSnapshot {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var snap wsSnapshot
	if err := conn.ReadJSON(&snap); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snap.Type != WSTypeSnapshot {
		t.Fatalf("first frame type=%q, want snapshot", snap.Type)
	}
	return snap
}

func readEvent(t *testing.T, conn *websocket.Conn, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		var evt map[string]any
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("read while waiting for %s: %v", want, err)
		}
		if evt["type"] == want {
			return evt
		}
	}
	t.Fatalf("did not receive %s within deadline", want)
	return nil
}

func mustWriteJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func createPublicRoom(t *testing.T, srv *httptest.Server, c *http.Client) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"visibility": "public", "name": "test",
	})
	req, _ := http.NewRequest("POST", srv.URL+"/api/rooms", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create room status=%d", resp.StatusCode)
	}
	var room Room
	json.NewDecoder(resp.Body).Decode(&room)
	return room.ID
}

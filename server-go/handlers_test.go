package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withSession returns a Cookie header carrying a freshly signed session
// for userID. Skips the /api/session round-trip in tests.
func withSession(t *testing.T, userID string) *http.Cookie {
	t.Helper()
	return &http.Cookie{
		Name:  sessionCookieName,
		Value: signCookie(userID, time.Now().Add(time.Hour)),
	}
}

func setupHandlerEnv(t *testing.T) *http.ServeMux {
	t.Helper()
	setupTestDB(t)
	setupSessionSecret(t)
	setupManager(t, time.Hour) // notify.go reads `manager.hubs`
	if err := seedLobby(); err != nil {
		t.Fatalf("seed lobby: %v", err)
	}
	return buildMux()
}

func sendJSON(t *testing.T, mux *http.ServeMux, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	r := httptest.NewRequest(method, path, rdr)
	if rdr != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// ─── /api/me ────────────────────────────────────────────────────

func TestGetMe_LazyCreatesUserRow(t *testing.T) {
	mux := setupHandlerEnv(t)
	w := sendJSON(t, mux, "GET", "/api/me", nil, withSession(t, "u1"))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var u User
	json.NewDecoder(w.Body).Decode(&u)
	if u.ID != "u1" || u.DisplayName != "" {
		t.Fatalf("got %+v", u)
	}

	var n int
	db.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", "u1").Scan(&n)
	if n != 1 {
		t.Fatalf("user row count=%d, want 1", n)
	}
}

func TestPutMe_RejectsEmptyOrLong(t *testing.T) {
	mux := setupHandlerEnv(t)
	cookie := withSession(t, "u1")

	for _, name := range []string{"", "   ", strings.Repeat("a", 21)} {
		w := sendJSON(t, mux, "PUT", "/api/me", map[string]string{"display_name": name}, cookie)
		if w.Code != 400 {
			t.Errorf("name=%q status=%d, want 400", name, w.Code)
		}
	}
}

func TestPutMe_UpdatesAndPersists(t *testing.T) {
	mux := setupHandlerEnv(t)
	cookie := withSession(t, "u1")

	w := sendJSON(t, mux, "PUT", "/api/me", map[string]string{"display_name": "  Foxy🦊  "}, cookie)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var u User
	json.NewDecoder(w.Body).Decode(&u)
	if u.DisplayName != "Foxy🦊" {
		t.Fatalf("display_name=%q (trim)", u.DisplayName)
	}

	// GET /api/me reflects the change.
	w2 := sendJSON(t, mux, "GET", "/api/me", nil, cookie)
	json.NewDecoder(w2.Body).Decode(&u)
	if u.DisplayName != "Foxy🦊" {
		t.Fatalf("after re-read=%q", u.DisplayName)
	}
}

// ─── room CRUD ──────────────────────────────────────────────────

func TestCreateRoom_OK(t *testing.T) {
	mux := setupHandlerEnv(t)
	cookie := withSession(t, "owner-1")

	w := sendJSON(t, mux, "POST", "/api/rooms", map[string]any{
		"visibility": "public", "name": "foo", "emoji_only": true,
	}, cookie)
	if w.Code != 201 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var room Room
	json.NewDecoder(w.Body).Decode(&room)
	if room.Kind != "group" || room.Visibility != "public" {
		t.Fatalf("got %+v", room)
	}
	if !room.EmojiOnly {
		t.Fatalf("emoji_only not propagated")
	}
	if room.OwnerID == nil || *room.OwnerID != "owner-1" {
		t.Fatalf("owner_id=%v, want owner-1", room.OwnerID)
	}

	// owner row exists with role=owner
	var role string
	db.QueryRow(
		"SELECT role FROM room_members WHERE room_id = ? AND user_id = ?",
		room.ID, "owner-1",
	).Scan(&role)
	if role != "owner" {
		t.Fatalf("role=%q", role)
	}
}

func TestCreateRoom_RejectsBadVisibility(t *testing.T) {
	mux := setupHandlerEnv(t)
	w := sendJSON(t, mux, "POST", "/api/rooms", map[string]any{
		"visibility": "secret", "name": "x",
	}, withSession(t, "u1"))
	if w.Code != 400 {
		t.Fatalf("status=%d", w.Code)
	}
}

func mustCreateRoom(t *testing.T, mux *http.ServeMux, owner, visibility string) string {
	t.Helper()
	w := sendJSON(t, mux, "POST", "/api/rooms",
		map[string]any{"visibility": visibility, "name": "test"},
		withSession(t, owner))
	if w.Code != 201 {
		t.Fatalf("create: %s", w.Body.String())
	}
	var room Room
	json.NewDecoder(w.Body).Decode(&room)
	return room.ID
}

func TestGetRoom_IncludesMyRole(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	t.Run("owner", func(t *testing.T) {
		w := sendJSON(t, mux, "GET", "/api/rooms/"+roomID, nil, withSession(t, "owner-1"))
		var d roomDetail
		json.NewDecoder(w.Body).Decode(&d)
		if d.MyRole != "owner" {
			t.Fatalf("my_role=%q, want owner", d.MyRole)
		}
		if d.MemberCount != 1 {
			t.Fatalf("member_count=%d, want 1", d.MemberCount)
		}
	})

	t.Run("non-member", func(t *testing.T) {
		w := sendJSON(t, mux, "GET", "/api/rooms/"+roomID, nil, withSession(t, "stranger"))
		var d roomDetail
		json.NewDecoder(w.Body).Decode(&d)
		if d.MyRole != "" {
			t.Fatalf("my_role=%q, want empty (non-member)", d.MyRole)
		}
	})
}

func TestPatchRoom_OwnerOnly(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	t.Run("non-owner forbidden", func(t *testing.T) {
		w := sendJSON(t, mux, "PATCH", "/api/rooms/"+roomID,
			map[string]any{"name": "hacked"},
			withSession(t, "stranger"))
		if w.Code != 403 {
			t.Fatalf("status=%d", w.Code)
		}
	})

	t.Run("owner ok", func(t *testing.T) {
		w := sendJSON(t, mux, "PATCH", "/api/rooms/"+roomID,
			map[string]any{"name": "renamed"},
			withSession(t, "owner-1"))
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var room Room
		json.NewDecoder(w.Body).Decode(&room)
		if room.Name != "renamed" {
			t.Fatalf("name=%q", room.Name)
		}
	})
}

// ─── join / leave / transfer / hide ─────────────────────────────

func TestJoin_PublicRoomOpen(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/join", nil, withSession(t, "joiner"))
	if w.Code != 204 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	role, _, isMember, _ := roomMembership(roomID, "joiner")
	if !isMember || role != "member" {
		t.Fatalf("isMember=%v role=%q", isMember, role)
	}
}

func TestJoin_PrivateRequiresInvite(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "private")

	t.Run("no token forbidden", func(t *testing.T) {
		w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/join", nil, withSession(t, "outsider"))
		if w.Code != 403 {
			t.Fatalf("status=%d", w.Code)
		}
	})

	t.Run("valid token ok", func(t *testing.T) {
		// Owner creates an invite.
		invW := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/invite",
			map[string]any{"create_link": true},
			withSession(t, "owner-1"))
		if invW.Code != 201 {
			t.Fatalf("invite: %s", invW.Body.String())
		}
		var inv map[string]string
		json.NewDecoder(invW.Body).Decode(&inv)

		w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/join",
			map[string]string{"invite_token": inv["token"]},
			withSession(t, "outsider"))
		if w.Code != 204 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("bad token forbidden", func(t *testing.T) {
		w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/join",
			map[string]string{"invite_token": "garbage"},
			withSession(t, "u3"))
		if w.Code != 403 {
			t.Fatalf("status=%d", w.Code)
		}
	})
}

func TestJoin_RespectsCap(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	// Pre-fill members directly to GroupMemberCap-1 (owner already counts as 1).
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < GroupMemberCap-1; i++ {
		uid := fmt.Sprintf("filler-%d", i)
		mustExec(t, "INSERT INTO room_members (room_id,user_id,role,joined_at) VALUES (?,?,?,?)",
			roomID, uid, RoleMember, now)
	}

	w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/join", nil, withSession(t, "overflow"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (full)", w.Code)
	}
}

func TestLeave_OwnerMustTransferIfOthers(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	// Add a second member.
	sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/join", nil, withSession(t, "u2"))

	w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/leave", nil, withSession(t, "owner-1"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (owner must transfer)", w.Code)
	}
}

func TestLeave_LastOwnerDeletesRoom(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/leave", nil, withSession(t, "owner-1"))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM rooms WHERE id = ?", roomID).Scan(&n)
	if n != 0 {
		t.Fatalf("room still exists")
	}
}

func TestLeave_LobbyForbidden(t *testing.T) {
	mux := setupHandlerEnv(t)
	w := sendJSON(t, mux, "POST", "/api/rooms/lobby/leave", nil, withSession(t, "u1"))
	if w.Code != 403 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestTransfer_HappyPath(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")
	sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/join", nil, withSession(t, "successor"))

	w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/transfer",
		map[string]string{"new_owner_id": "successor"},
		withSession(t, "owner-1"))
	if w.Code != 204 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	room, _ := getRoom(roomID)
	if room.OwnerID == nil || *room.OwnerID != "successor" {
		t.Fatalf("owner=%v", room.OwnerID)
	}
	role, _, _, _ := roomMembership(roomID, "owner-1")
	if role != "member" {
		t.Fatalf("old owner role=%q, want member", role)
	}
}

func TestTransfer_TargetMustBeMember(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/transfer",
		map[string]string{"new_owner_id": "stranger"},
		withSession(t, "owner-1"))
	if w.Code != 400 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHide_LobbyRejected(t *testing.T) {
	mux := setupHandlerEnv(t)
	w := sendJSON(t, mux, "POST", "/api/rooms/lobby/hide", nil, withSession(t, "u1"))
	if w.Code != 403 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHide_TogglesFlag(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/hide", nil, withSession(t, "owner-1"))
	_, hidden, _, _ := roomMembership(roomID, "owner-1")
	if !hidden {
		t.Fatalf("hidden flag not set")
	}
	sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/unhide", nil, withSession(t, "owner-1"))
	_, hidden, _, _ = roomMembership(roomID, "owner-1")
	if hidden {
		t.Fatalf("hidden flag not cleared")
	}
}

// ─── members / public list / invite preview ─────────────────────

func TestGetMembers_GroupOnlyForMembers(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "public")

	t.Run("non-member forbidden", func(t *testing.T) {
		w := sendJSON(t, mux, "GET", "/api/rooms/"+roomID+"/members", nil, withSession(t, "stranger"))
		if w.Code != 403 {
			t.Fatalf("status=%d", w.Code)
		}
	})

	t.Run("member ok", func(t *testing.T) {
		w := sendJSON(t, mux, "GET", "/api/rooms/"+roomID+"/members", nil, withSession(t, "owner-1"))
		if w.Code != 200 {
			t.Fatalf("status=%d", w.Code)
		}
		var resp struct {
			Members []Member `json:"members"`
		}
		json.NewDecoder(w.Body).Decode(&resp)
		if len(resp.Members) != 1 {
			t.Fatalf("members=%d, want 1", len(resp.Members))
		}
		if resp.Members[0].Role != "owner" {
			t.Fatalf("role=%q", resp.Members[0].Role)
		}
	})
}

func TestGetMembers_LobbyImplicit(t *testing.T) {
	mux := setupHandlerEnv(t)

	// Force a few user rows to populate the lobby member listing.
	for _, id := range []string{"u1", "u2", "u3"} {
		ensureUser(id)
	}

	w := sendJSON(t, mux, "GET", "/api/rooms/lobby/members", nil, withSession(t, "u1"))
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var resp struct {
		Members []Member `json:"members"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Members) < 3 {
		t.Fatalf("lobby members=%d, want >=3", len(resp.Members))
	}
}

func TestGetPublicRooms(t *testing.T) {
	mux := setupHandlerEnv(t)
	mustCreateRoom(t, mux, "owner-1", "public")
	mustCreateRoom(t, mux, "owner-2", "private") // should NOT appear
	mustCreateRoom(t, mux, "owner-3", "public")

	w := sendJSON(t, mux, "GET", "/api/rooms/public", nil, withSession(t, "browser"))
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var resp struct {
		Rooms []PublicRoomEntry `json:"rooms"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	// Lobby itself is also public+lobby kind; we filter to kind=group, so it
	// should NOT appear here.
	for _, e := range resp.Rooms {
		if e.Room.Kind != "group" || e.Room.Visibility != "public" {
			t.Fatalf("unexpected entry %+v", e)
		}
	}
	if len(resp.Rooms) != 2 {
		t.Fatalf("public groups=%d, want 2", len(resp.Rooms))
	}
}

func TestGetInvitePreview(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "private")

	invW := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/invite",
		map[string]any{"create_link": true},
		withSession(t, "owner-1"))
	var inv map[string]string
	json.NewDecoder(invW.Body).Decode(&inv)

	w := sendJSON(t, mux, "GET", "/api/invites/"+inv["token"], nil, nil)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var preview struct {
		Token string `json:"token"`
		Room  Room   `json:"room"`
	}
	json.NewDecoder(w.Body).Decode(&preview)
	if preview.Room.ID != roomID {
		t.Fatalf("room id mismatch")
	}
}

func TestInvite_NonMemberRejected(t *testing.T) {
	mux := setupHandlerEnv(t)
	roomID := mustCreateRoom(t, mux, "owner-1", "private")

	w := sendJSON(t, mux, "POST", "/api/rooms/"+roomID+"/invite",
		map[string]any{"create_link": true},
		withSession(t, "stranger"))
	if w.Code != 403 {
		t.Fatalf("status=%d", w.Code)
	}
}

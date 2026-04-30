package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withSession returns a Cookie header value carrying a freshly signed
// session for the given userID. Useful to skip going through /api/session.
func withSession(t *testing.T, userID string) *http.Cookie {
	t.Helper()
	return &http.Cookie{
		Name:  sessionCookieName,
		Value: signCookie(userID, time.Now().Add(time.Hour)),
	}
}

func TestHandleCreateRoom_RequiresSession(t *testing.T) {
	setupTestDB(t)
	setupSessionSecret(t)

	r := httptest.NewRequest("POST", "/api/rooms", strings.NewReader(`{"name":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleCreateRoom(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", w.Code)
	}
}

func TestHandleCreateRoom_PersistsRoomWithCallerAsOwner(t *testing.T) {
	setupTestDB(t)
	setupSessionSecret(t)

	userID := "owner-1"
	body := bytes.NewBufferString(`{"name":"💬"}`)
	r := httptest.NewRequest("POST", "/api/rooms", body)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(withSession(t, userID))
	w := httptest.NewRecorder()

	handleCreateRoom(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201; body=%s", w.Code, w.Body.String())
	}

	var room Room
	if err := json.NewDecoder(w.Body).Decode(&room); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if room.OwnerID != userID {
		t.Fatalf("owner_id=%q, want %q", room.OwnerID, userID)
	}
	if room.Name != "💬" {
		t.Fatalf("name=%q, want %q", room.Name, "💬")
	}
	if room.ID == "" {
		t.Fatal("id missing")
	}

	// Verify the row landed in the DB.
	var ownerID string
	if err := db.QueryRow("SELECT owner_id FROM rooms WHERE id = ?", room.ID).Scan(&ownerID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if ownerID != userID {
		t.Fatalf("DB owner_id=%q, want %q", ownerID, userID)
	}
}

func TestHandleCreateRoom_IgnoresClientOwnerID(t *testing.T) {
	// Clients used to send `owner_id` in the body. The server must derive
	// owner from the signed cookie and ignore the body field — otherwise
	// the session-cookie security model collapses.
	setupTestDB(t)
	setupSessionSecret(t)

	caller := "real-owner"
	spoofed := "attacker-owner"
	body := bytes.NewBufferString(`{"name":"X","owner_id":"` + spoofed + `"}`)
	r := httptest.NewRequest("POST", "/api/rooms", body)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(withSession(t, caller))
	w := httptest.NewRecorder()

	handleCreateRoom(w, r)

	var room Room
	if err := json.NewDecoder(w.Body).Decode(&room); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if room.OwnerID != caller {
		t.Fatalf("owner_id=%q, want %q (server must trust cookie, not body)", room.OwnerID, caller)
	}
}

func mustCreateRoom(t *testing.T, ownerID string) string {
	t.Helper()
	body := bytes.NewBufferString(`{"name":"test"}`)
	r := httptest.NewRequest("POST", "/api/rooms", body)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(withSession(t, ownerID))
	w := httptest.NewRecorder()
	handleCreateRoom(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create room: status=%d body=%s", w.Code, w.Body.String())
	}
	var room Room
	json.NewDecoder(w.Body).Decode(&room)
	return room.ID
}

func TestHandleGetRoom(t *testing.T) {
	setupTestDB(t)
	setupSessionSecret(t)

	roomID := mustCreateRoom(t, "owner-1")

	mux := buildMux()

	t.Run("existing room", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/rooms/"+roomID, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200", w.Code)
		}
		var room Room
		if err := json.NewDecoder(w.Body).Decode(&room); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if room.ID != roomID {
			t.Fatalf("id=%q, want %q", room.ID, roomID)
		}
	})

	t.Run("missing room", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/rooms/does-not-exist", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status=%d, want 404", w.Code)
		}
	})
}

func TestHandleDeleteRoom(t *testing.T) {
	setupTestDB(t)
	setupSessionSecret(t)

	owner := "owner-1"
	other := "other-user"
	roomID := mustCreateRoom(t, owner)

	mux := buildMux()

	t.Run("non-owner forbidden", func(t *testing.T) {
		r := httptest.NewRequest("DELETE", "/api/rooms/"+roomID, nil)
		r.AddCookie(withSession(t, other))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d, want 403; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("missing session unauthorized", func(t *testing.T) {
		r := httptest.NewRequest("DELETE", "/api/rooms/"+roomID, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d, want 401", w.Code)
		}
	})

	t.Run("owner deletes", func(t *testing.T) {
		r := httptest.NewRequest("DELETE", "/api/rooms/"+roomID, nil)
		r.AddCookie(withSession(t, owner))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("status=%d, want 204; body=%s", w.Code, w.Body.String())
		}
		var count int
		db.QueryRow("SELECT COUNT(*) FROM rooms WHERE id = ?", roomID).Scan(&count)
		if count != 0 {
			t.Fatalf("room still in DB after delete")
		}
	})

	t.Run("missing room", func(t *testing.T) {
		r := httptest.NewRequest("DELETE", "/api/rooms/does-not-exist", nil)
		r.AddCookie(withSession(t, owner))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status=%d, want 404", w.Code)
		}
	})
}

func TestHandleGetMessages(t *testing.T) {
	setupTestDB(t)
	setupSessionSecret(t)

	owner := "owner-1"
	roomID := mustCreateRoom(t, owner)
	mux := buildMux()

	// Seed 3 messages directly so we don't need the WS pump for this test.
	for _, content := range []string{"😀", "🎉", "🚀"} {
		if _, err := db.Exec(
			"INSERT INTO messages (room_id, user_id, content, created_at) VALUES (?,?,?,?)",
			roomID, owner, content, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	t.Run("returns chronological", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/rooms/"+roomID+"/messages?limit=10", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d", w.Code)
		}
		var body struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(w.Body).Decode(&body)
		if len(body.Messages) != 3 {
			t.Fatalf("got %d messages, want 3", len(body.Messages))
		}
		if body.Messages[0].Content != "😀" || body.Messages[2].Content != "🚀" {
			t.Fatalf("messages not chronological: %+v", body.Messages)
		}
	})

	t.Run("respects before cursor", func(t *testing.T) {
		// Get all first to find the second message's id.
		r := httptest.NewRequest("GET", "/api/rooms/"+roomID+"/messages?limit=10", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var all struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(w.Body).Decode(&all)
		secondID := all.Messages[1].ID

		r2 := httptest.NewRequest("GET", "/api/rooms/"+roomID+"/messages?before="+itoa(secondID), nil)
		w2 := httptest.NewRecorder()
		mux.ServeHTTP(w2, r2)
		var page struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(w2.Body).Decode(&page)
		if len(page.Messages) != 1 {
			t.Fatalf("got %d messages before id=%d, want 1", len(page.Messages), secondID)
		}
		if page.Messages[0].Content != "😀" {
			t.Fatalf("got %q, want first message", page.Messages[0].Content)
		}
	})

	t.Run("empty room", func(t *testing.T) {
		emptyID := mustCreateRoom(t, owner)
		r := httptest.NewRequest("GET", "/api/rooms/"+emptyID+"/messages", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var body struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(w.Body).Decode(&body)
		if len(body.Messages) != 0 {
			t.Fatalf("got %d messages, want 0", len(body.Messages))
		}
	})
}

func TestHandleGetConfig(t *testing.T) {
	t.Setenv("PUBLIC_ORIGIN", "https://example.com")

	r := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	handleGetConfig(w, r)

	var body struct {
		PublicOrigin string `json:"public_origin"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if body.PublicOrigin != "https://example.com" {
		t.Fatalf("public_origin=%q, want %q", body.PublicOrigin, "https://example.com")
	}
}

func itoa(n int64) string {
	// stdlib's strconv would be simpler but I want to avoid importing it
	// just for the tests; a tiny helper keeps the import surface minimal.
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

package main

import (
	"testing"
)

// TestSchema_TablesExist verifies all expected tables are created by
// createSQLiteTables. Sanity check that the schema migration is wired up.
func TestSchema_TablesExist(t *testing.T) {
	setupTestDB(t)

	want := []string{"users", "rooms", "room_members", "messages", "invites"}
	for _, name := range want {
		var found string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name = ?", name,
		).Scan(&found)
		if err != nil {
			t.Errorf("table %q missing: %v", name, err)
		}
	}
}

// TestSchema_LobbySeeded verifies seedLobby installs the singleton lobby
// row with the expected attributes.
func TestSchema_LobbySeeded(t *testing.T) {
	setupTestDB(t)
	if err := seedLobby(); err != nil {
		t.Fatalf("seedLobby: %v", err)
	}

	var (
		id, kind, visibility, name string
		emojiOnly                  int
		ownerID                    *string
	)
	err := db.QueryRow(
		"SELECT id, kind, visibility, name, emoji_only, owner_id FROM rooms WHERE id = ?",
		LobbyRoomID,
	).Scan(&id, &kind, &visibility, &name, &emojiOnly, &ownerID)
	if err != nil {
		t.Fatalf("lobby row not found: %v", err)
	}
	if kind != RoomKindLobby {
		t.Errorf("lobby kind=%q, want %q", kind, RoomKindLobby)
	}
	if visibility != RoomVisibilityPublic {
		t.Errorf("lobby visibility=%q, want %q", visibility, RoomVisibilityPublic)
	}
	if ownerID != nil {
		t.Errorf("lobby owner_id=%v, want nil", ownerID)
	}
	if name == "" {
		t.Errorf("lobby name unexpectedly empty")
	}
}

// TestSchema_LobbySeedIsIdempotent — running seed twice must not duplicate
// the lobby row or error out.
func TestSchema_LobbySeedIsIdempotent(t *testing.T) {
	setupTestDB(t)
	for i := 0; i < 3; i++ {
		if err := seedLobby(); err != nil {
			t.Fatalf("seedLobby pass %d: %v", i, err)
		}
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM rooms WHERE id = ?", LobbyRoomID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("lobby row count=%d, want 1", n)
	}
}

// TestSchema_RoomMemberCascade — deleting a room should cascade to its
// members and messages (FK ON DELETE CASCADE on the new schema).
func TestSchema_RoomMemberCascade(t *testing.T) {
	setupTestDB(t)

	now := "2026-04-30T00:00:00Z"
	mustExec(t, "INSERT INTO rooms (id,kind,visibility,name,emoji_only,owner_id,created_at) VALUES (?,?,?,?,0,NULL,?)",
		"r1", RoomKindGroup, RoomVisibilityPublic, "g", now)
	mustExec(t, "INSERT INTO room_members (room_id,user_id,role,joined_at) VALUES (?,?,?,?)",
		"r1", "u1", RoleMember, now)
	mustExec(t, "INSERT INTO messages (room_id,user_id,content,created_at) VALUES (?,?,?,?)",
		"r1", "u1", "hi", now)

	mustExec(t, "DELETE FROM rooms WHERE id = ?", "r1")

	var n int
	db.QueryRow("SELECT COUNT(*) FROM room_members WHERE room_id = ?", "r1").Scan(&n)
	if n != 0 {
		t.Errorf("room_members rows after cascade=%d, want 0", n)
	}
	db.QueryRow("SELECT COUNT(*) FROM messages WHERE room_id = ?", "r1").Scan(&n)
	if n != 0 {
		t.Errorf("messages rows after cascade=%d, want 0", n)
	}
}

func mustExec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

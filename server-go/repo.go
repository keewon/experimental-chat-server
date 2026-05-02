package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// errNotFound is returned by repo functions when a row does not exist.
// Handlers map this to HTTP 404.
var errNotFound = errors.New("not found")

// ─── users ──────────────────────────────────────────────────────

// ensureUser returns the user row for userID, lazily creating it with an
// empty display_name if missing. Sessions are issued at cookie creation
// time; the user row is materialized on the first authenticated social
// action (so tab-only visitors who never act don't pollute the table).
func ensureUser(userID string) (User, error) {
	u, err := getUser(userID)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, errNotFound) {
		return User{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		"INSERT INTO users (id, display_name, created_at) VALUES (?, '', ?)",
		userID, now,
	); err != nil {
		// Concurrent first-action races could both try to insert; on a
		// uniqueness violation we simply re-read.
		if isUniqueViolation(err) {
			return getUser(userID)
		}
		return User{}, err
	}
	return User{ID: userID, DisplayName: "", CreatedAt: now}, nil
}

func getUser(userID string) (User, error) {
	var u User
	err := db.QueryRow(
		"SELECT id, display_name, created_at FROM users WHERE id = ?", userID,
	).Scan(&u.ID, &u.DisplayName, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, errNotFound
	}
	return u, err
}

func updateDisplayName(userID, name string) error {
	_, err := db.Exec("UPDATE users SET display_name = ? WHERE id = ?", name, userID)
	return err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}

// ─── rooms ──────────────────────────────────────────────────────

func getRoom(roomID string) (Room, error) {
	var r Room
	var emojiOnly int
	err := db.QueryRow(
		"SELECT id, kind, visibility, name, emoji_only, owner_id, created_at FROM rooms WHERE id = ?",
		roomID,
	).Scan(&r.ID, &r.Kind, &r.Visibility, &r.Name, &emojiOnly, &r.OwnerID, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, errNotFound
	}
	r.EmojiOnly = emojiOnly != 0
	return r, err
}

// roomMembership returns the caller's role for the given room.
// Lobby is implicit-membership: every authenticated user is a member.
func roomMembership(roomID, userID string) (role string, hidden bool, isMember bool, err error) {
	if roomID == LobbyRoomID {
		return RoleMember, false, true, nil
	}
	err = db.QueryRow(
		"SELECT role, hidden FROM room_members WHERE room_id = ? AND user_id = ?",
		roomID, userID,
	).Scan(&role, &hidden)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	return role, hidden, true, nil
}

// memberCount returns the number of members. Lobby returns the total
// user count (since membership is implicit for all users).
func memberCount(roomID string) (int, error) {
	var n int
	var err error
	if roomID == LobbyRoomID {
		err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	} else {
		err = db.QueryRow("SELECT COUNT(*) FROM room_members WHERE room_id = ?", roomID).Scan(&n)
	}
	return n, err
}

// listMembers returns members joined with display names. Lobby returns
// up to `limit` users from the global users table (ordered most-recent
// first) since explicit lobby memberships are not stored.
func listMembers(roomID string, limit int) ([]Member, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if roomID == LobbyRoomID {
		rows, err = db.Query(`
			SELECT u.id, u.display_name, ?, u.created_at
			FROM users u
			ORDER BY u.created_at DESC
			LIMIT ?`, RoleMember, limit)
	} else {
		rows, err = db.Query(`
			SELECT m.user_id, COALESCE(u.display_name, ''), m.role, m.joined_at
			FROM room_members m
			LEFT JOIN users u ON u.id = m.user_id
			WHERE m.room_id = ?
			ORDER BY m.joined_at ASC`, roomID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Member, 0)
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// addMemberWithCap inserts a member row, enforcing GroupMemberCap.
// Returns errRoomFull if the cap would be exceeded. Idempotent on the
// PK conflict (already a member → returns nil).
var errRoomFull = errors.New("room is full")

func addMemberWithCap(roomID, userID, role string) error {
	if roomID == LobbyRoomID {
		return nil // implicit
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM room_members WHERE room_id = ?", roomID).Scan(&n); err != nil {
		return err
	}
	// If they're already a member, treat as success.
	var existingRole string
	err = tx.QueryRow("SELECT role FROM room_members WHERE room_id = ? AND user_id = ?", roomID, userID).Scan(&existingRole)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if n >= GroupMemberCap {
		return errRoomFull
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(
		"INSERT INTO room_members (room_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)",
		roomID, userID, role, now,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func removeMember(roomID, userID string) error {
	_, err := db.Exec("DELETE FROM room_members WHERE room_id = ? AND user_id = ?", roomID, userID)
	return err
}

func setMemberHidden(roomID, userID string, hidden bool) error {
	v := 0
	if hidden {
		v = 1
	}
	_, err := db.Exec(
		"UPDATE room_members SET hidden = ? WHERE room_id = ? AND user_id = ?",
		v, roomID, userID,
	)
	return err
}

func setRoomOwner(roomID, newOwnerID string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// New owner must already be a member.
	var n int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM room_members WHERE room_id = ? AND user_id = ?",
		roomID, newOwnerID,
	).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return errNotFound
	}
	if _, err := tx.Exec("UPDATE rooms SET owner_id = ? WHERE id = ?", newOwnerID, roomID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"UPDATE room_members SET role = ? WHERE room_id = ?", RoleMember, roomID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"UPDATE room_members SET role = ? WHERE room_id = ? AND user_id = ?",
		RoleOwner, roomID, newOwnerID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteRoom(roomID string) error {
	_, err := db.Exec("DELETE FROM rooms WHERE id = ?", roomID)
	return err
}

// patchRoom updates name and emoji_only fields on a room. Pass nil to
// leave a field unchanged.
func patchRoom(roomID string, name *string, emojiOnly *bool) error {
	sets := []string{}
	args := []any{}
	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if emojiOnly != nil {
		v := 0
		if *emojiOnly {
			v = 1
		}
		sets = append(sets, "emoji_only = ?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, roomID)
	_, err := db.Exec(
		fmt.Sprintf("UPDATE rooms SET %s WHERE id = ?", strings.Join(sets, ", ")),
		args...,
	)
	return err
}

// ─── invites ────────────────────────────────────────────────────

type Invite struct {
	Token     string `json:"token"`
	RoomID    string `json:"room_id"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	Revoked   bool   `json:"revoked"`
}

func createInvite(roomID, createdBy, token string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		"INSERT INTO invites (token, room_id, created_by, created_at) VALUES (?, ?, ?, ?)",
		token, roomID, createdBy, now,
	)
	return err
}

func getInvite(token string) (Invite, error) {
	var inv Invite
	var revokedAt sql.NullString
	err := db.QueryRow(
		"SELECT token, room_id, created_by, created_at, revoked_at FROM invites WHERE token = ?",
		token,
	).Scan(&inv.Token, &inv.RoomID, &inv.CreatedBy, &inv.CreatedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Invite{}, errNotFound
	}
	if err != nil {
		return Invite{}, err
	}
	inv.Revoked = revokedAt.Valid
	return inv, nil
}

// ─── messages ───────────────────────────────────────────────────

// listMessages returns at most `limit` messages for a room, optionally
// older than `before` (msg id). Display names are LEFT JOINed so anonymous
// senders (no users row) still appear with an empty name.
func listMessages(roomID string, before int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if before > 0 {
		rows, err = db.Query(`
			SELECT m.id, m.room_id, m.user_id, COALESCE(u.display_name,''), m.content, m.created_at
			FROM messages m LEFT JOIN users u ON u.id = m.user_id
			WHERE m.room_id = ? AND m.id < ?
			ORDER BY m.id DESC LIMIT ?`, roomID, before, limit)
	} else {
		rows, err = db.Query(`
			SELECT m.id, m.room_id, m.user_id, COALESCE(u.display_name,''), m.content, m.created_at
			FROM messages m LEFT JOIN users u ON u.id = m.user_id
			WHERE m.room_id = ?
			ORDER BY m.id DESC LIMIT ?`, roomID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Message, 0)
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.RoomID, &m.UserID, &m.DisplayName, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	// Reverse to chronological for the API response.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func lastMessageForRoom(roomID string) (*Message, error) {
	var m Message
	err := db.QueryRow(`
		SELECT m.id, m.room_id, m.user_id, COALESCE(u.display_name,''), m.content, m.created_at
		FROM messages m LEFT JOIN users u ON u.id = m.user_id
		WHERE m.room_id = ?
		ORDER BY m.id DESC LIMIT 1`, roomID,
	).Scan(&m.ID, &m.RoomID, &m.UserID, &m.DisplayName, &m.Content, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// myRoomsForSnapshot returns every room the user is a member of plus the
// lobby (which is implicit-membership). Each entry carries the user's
// role, sidebar-hidden flag, and the latest message preview.
func myRoomsForSnapshot(userID string) ([]wsRoomEntry, error) {
	rows, err := db.Query(`
		SELECT r.id, r.kind, r.visibility, r.name, r.emoji_only, r.owner_id, r.created_at,
			m.role, m.hidden
		FROM rooms r INNER JOIN room_members m ON m.room_id = r.id
		WHERE m.user_id = ? AND r.kind != ?`,
		userID, RoomKindLobby,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]wsRoomEntry, 0)
	for rows.Next() {
		var e wsRoomEntry
		var emojiOnly, hidden int
		if err := rows.Scan(
			&e.Room.ID, &e.Room.Kind, &e.Room.Visibility, &e.Room.Name,
			&emojiOnly, &e.Room.OwnerID, &e.Room.CreatedAt,
			&e.MyRole, &hidden,
		); err != nil {
			return nil, err
		}
		e.Room.EmojiOnly = emojiOnly != 0
		e.Hidden = hidden != 0
		out = append(out, e)
	}

	// Always include the lobby with implicit member role.
	if lobby, err := getRoom(LobbyRoomID); err == nil {
		out = append(out, wsRoomEntry{Room: lobby, MyRole: RoleMember})
	}

	for i := range out {
		if msg, err := lastMessageForRoom(out[i].Room.ID); err == nil && msg != nil {
			out[i].LastMessage = msg
		}
	}
	return out, nil
}

// ─── public room list ───────────────────────────────────────────

type PublicRoomEntry struct {
	Room        Room  `json:"room"`
	MemberCount int   `json:"member_count"`
	LastMsgID   int64 `json:"last_msg_id,omitempty"`
}

func listPublicRooms(limit, offset int) ([]PublicRoomEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.Query(`
		SELECT r.id, r.kind, r.visibility, r.name, r.emoji_only, r.owner_id, r.created_at,
			(SELECT COUNT(*) FROM room_members WHERE room_id = r.id) AS member_count,
			COALESCE((SELECT MAX(id) FROM messages WHERE room_id = r.id), 0) AS last_msg_id
		FROM rooms r
		WHERE r.kind = ? AND r.visibility = ?
		ORDER BY last_msg_id DESC, r.created_at DESC
		LIMIT ? OFFSET ?`,
		RoomKindGroup, RoomVisibilityPublic, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]PublicRoomEntry, 0)
	for rows.Next() {
		var e PublicRoomEntry
		var emojiOnly int
		if err := rows.Scan(
			&e.Room.ID, &e.Room.Kind, &e.Room.Visibility, &e.Room.Name,
			&emojiOnly, &e.Room.OwnerID, &e.Room.CreatedAt,
			&e.MemberCount, &e.LastMsgID,
		); err != nil {
			return nil, err
		}
		e.Room.EmojiOnly = emojiOnly != 0
		out = append(out, e)
	}
	return out, nil
}

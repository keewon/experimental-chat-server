package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	displayNameMaxRunes = 20
	roomNameMaxRunes    = 50
)

// ─── /api/me ────────────────────────────────────────────────────

func handleGetMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	u, err := ensureUser(userID)
	if err != nil {
		log.Print(err)
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, u, http.StatusOK)
}

func handlePutMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if name == "" {
		jsonError(w, "display_name required", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(name) > displayNameMaxRunes {
		jsonError(w, "display_name too long", http.StatusBadRequest)
		return
	}
	if _, err := ensureUser(userID); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if err := updateDisplayName(userID, name); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	u, _ := getUser(userID)
	emitNameChanged(userID, name)
	jsonResponse(w, u, http.StatusOK)
}

// ─── /api/rooms ─────────────────────────────────────────────────

func handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		Visibility string `json:"visibility"`
		Name       string `json:"name"`
		EmojiOnly  bool   `json:"emoji_only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Visibility != RoomVisibilityPublic && req.Visibility != RoomVisibilityPrivate {
		jsonError(w, "visibility must be 'public' or 'private'", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		jsonError(w, "name required", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(name) > roomNameMaxRunes {
		jsonError(w, "name too long", http.StatusBadRequest)
		return
	}
	if _, err := ensureUser(userID); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}

	roomID := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := db.Begin()
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	emoji := 0
	if req.EmojiOnly {
		emoji = 1
	}
	if _, err := tx.Exec(
		`INSERT INTO rooms (id, kind, visibility, name, emoji_only, owner_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, RoomKindGroup, req.Visibility, name, emoji, userID, now,
	); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO room_members (room_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)`,
		roomID, userID, RoleOwner, now,
	); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}

	owner := userID
	room := Room{
		ID: roomID, Kind: RoomKindGroup, Visibility: req.Visibility,
		Name: name, EmojiOnly: req.EmojiOnly, OwnerID: &owner, CreatedAt: now,
	}
	emitRoomCreated(userID, room)
	jsonResponse(w, room, http.StatusCreated)
}

// roomDetail is the GET /api/rooms/{id} response — room metadata plus the
// caller's role (or empty when not a member).
type roomDetail struct {
	Room        Room   `json:"room"`
	MyRole      string `json:"my_role,omitempty"`   // "owner" / "member" / "" if not a member
	MemberCount int    `json:"member_count"`
	Hidden      bool   `json:"hidden"`
}

func handleGetRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")
	if roomID == "" {
		jsonError(w, "room id required", http.StatusBadRequest)
		return
	}
	room, err := getRoom(roomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	role, hidden, _, err := roomMembership(roomID, userID)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	count, err := memberCount(roomID)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, roomDetail{
		Room: room, MyRole: role, MemberCount: count, Hidden: hidden,
	}, http.StatusOK)
}

func handlePatchRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")
	room, err := getRoom(roomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if room.Kind != RoomKindGroup {
		jsonError(w, "only group rooms can be edited", http.StatusForbidden)
		return
	}
	if room.OwnerID == nil || *room.OwnerID != userID {
		jsonError(w, "only the owner can edit the room", http.StatusForbidden)
		return
	}

	var req struct {
		Name      *string `json:"name"`
		EmojiOnly *bool   `json:"emoji_only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || utf8.RuneCountInString(n) > roomNameMaxRunes {
			jsonError(w, "name must be 1-50 chars", http.StatusBadRequest)
			return
		}
		req.Name = &n
	}
	if err := patchRoom(roomID, req.Name, req.EmojiOnly); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	updated, _ := getRoom(roomID)
	emitRoomUpdated(updated)
	jsonResponse(w, updated, http.StatusOK)
}

func handleTransferOwner(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")
	room, err := getRoom(roomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if room.Kind != RoomKindGroup {
		jsonError(w, "only group rooms have owners", http.StatusForbidden)
		return
	}
	if room.OwnerID == nil || *room.OwnerID != userID {
		jsonError(w, "only the owner can transfer ownership", http.StatusForbidden)
		return
	}

	var req struct {
		NewOwnerID string `json:"new_owner_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.NewOwnerID == "" || req.NewOwnerID == userID {
		jsonError(w, "new_owner_id must be a different member", http.StatusBadRequest)
		return
	}
	if err := setRoomOwner(roomID, req.NewOwnerID); err != nil {
		if errors.Is(err, errNotFound) {
			jsonError(w, "target user is not a member", http.StatusBadRequest)
			return
		}
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	emitOwnerChanged(roomID, req.NewOwnerID)
	w.WriteHeader(http.StatusNoContent)
}

func handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")
	room, err := getRoom(roomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if room.Kind == RoomKindLobby {
		jsonError(w, "cannot leave the lobby", http.StatusForbidden)
		return
	}

	role, _, isMember, err := roomMembership(roomID, userID)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if !isMember {
		jsonError(w, "not a member", http.StatusForbidden)
		return
	}
	if role == RoleOwner {
		count, _ := memberCount(roomID)
		if count > 1 {
			jsonError(w, "owner must transfer before leaving", http.StatusConflict)
			return
		}
		if err := deleteRoom(roomID); err != nil {
			jsonError(w, "database error", http.StatusInternalServerError)
			return
		}
		emitRoomDeleted(roomID)
		jsonResponse(w, map[string]string{"result": "room_deleted"}, http.StatusOK)
		return
	}
	if err := removeMember(roomID, userID); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	emitMemberLeave(roomID, userID)
	w.WriteHeader(http.StatusNoContent)
}

func handleHideRoom(w http.ResponseWriter, r *http.Request)   { setHide(w, r, true) }
func handleUnhideRoom(w http.ResponseWriter, r *http.Request) { setHide(w, r, false) }

func setHide(w http.ResponseWriter, r *http.Request, hidden bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")
	room, err := getRoom(roomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if room.Kind == RoomKindLobby {
		jsonError(w, "cannot hide the lobby", http.StatusForbidden)
		return
	}
	_, _, isMember, err := roomMembership(roomID, userID)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if !isMember {
		jsonError(w, "not a member", http.StatusForbidden)
		return
	}
	if err := setMemberHidden(roomID, userID, hidden); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleJoinRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")
	room, err := getRoom(roomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if room.Kind == RoomKindLobby {
		// Lobby membership is implicit; treat as no-op success.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var req struct {
		InviteToken string `json:"invite_token"`
	}
	json.NewDecoder(r.Body).Decode(&req) // body optional

	if room.Visibility == RoomVisibilityPrivate {
		if req.InviteToken == "" {
			jsonError(w, "invite_token required for private rooms", http.StatusForbidden)
			return
		}
		inv, err := getInvite(req.InviteToken)
		if errors.Is(err, errNotFound) {
			jsonError(w, "invalid invite", http.StatusForbidden)
			return
		}
		if err != nil {
			jsonError(w, "database error", http.StatusInternalServerError)
			return
		}
		if inv.Revoked || inv.RoomID != roomID {
			jsonError(w, "invalid invite", http.StatusForbidden)
			return
		}
	}

	if _, err := ensureUser(userID); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if err := addMemberWithCap(roomID, userID, RoleMember); err != nil {
		if errors.Is(err, errRoomFull) {
			jsonError(w, "room is full", http.StatusConflict)
			return
		}
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	emitMemberJoin(roomID, userID)
	w.WriteHeader(http.StatusNoContent)
}

func handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")
	room, err := getRoom(roomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if room.Kind != RoomKindGroup {
		jsonError(w, "invites are only for group rooms", http.StatusForbidden)
		return
	}
	_, _, isMember, err := roomMembership(roomID, userID)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if !isMember {
		jsonError(w, "only members can create invites", http.StatusForbidden)
		return
	}

	token := uuid.New().String()
	if err := createInvite(roomID, userID, token); err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"token": token, "room_id": roomID}, http.StatusCreated)
}

func handleGetMembers(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	roomID := r.PathValue("id")
	room, err := getRoom(roomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	// Only members of the room can list its members. Lobby is implicit-
	// member for everyone, so always allowed.
	if room.Kind != RoomKindLobby {
		_, _, isMember, err := roomMembership(roomID, userID)
		if err != nil {
			jsonError(w, "database error", http.StatusInternalServerError)
			return
		}
		if !isMember {
			jsonError(w, "not a member", http.StatusForbidden)
			return
		}
	}

	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	members, err := listMembers(roomID, limit)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"members": members}, http.StatusOK)
}

func handleGetPublicRooms(w http.ResponseWriter, r *http.Request) {
	limit := 30
	offset := 0
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = l
	}
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil {
		offset = o
	}
	rooms, err := listPublicRooms(limit, offset)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"rooms": rooms}, http.StatusOK)
}

func handleGetInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := getInvite(token)
	if errors.Is(err, errNotFound) {
		jsonError(w, "invite not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if inv.Revoked {
		jsonError(w, "invite revoked", http.StatusGone)
		return
	}
	room, err := getRoom(inv.RoomID)
	if errors.Is(err, errNotFound) {
		jsonError(w, "room no longer exists", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	count, _ := memberCount(inv.RoomID)
	jsonResponse(w, map[string]any{
		"token":        inv.Token,
		"room":         room,
		"member_count": count,
	}, http.StatusOK)
}

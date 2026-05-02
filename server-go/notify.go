package main

import (
	"encoding/json"
	"log"
	"time"
)

// Step 4 — REST → WS bridge.
//
// REST handlers mutate state synchronously, then call into these helpers
// to keep all of the affected user's active WebSocket connections in
// sync without requiring a reconnect:
//   • attach/detach hubs as memberships change,
//   • push frames to the user's own clients (e.g. room_created),
//   • broadcast frames to a room's hub for everyone connected to that room,
//   • update each connection's cached display_name when the user renames.

// notifyUser pushes data to every active connection of userID.
// Buffer-full sends are dropped for that connection (the client is
// disconnected; readPump will tear it down soon).
func notifyUser(userID string, data []byte) {
	clientRegistry.forUser(userID, func(c *Client) {
		select {
		case c.send <- data:
		default:
		}
	})
}

// notifyRoom broadcasts to all clients connected to the room's hub.
// No-op if the hub does not exist (no one is connected).
func notifyRoom(roomID string, data []byte) {
	if v, ok := manager.hubs.Load(roomID); ok {
		h := v.(*RoomHub)
		select {
		case h.broadcast <- data:
		case <-h.done:
		}
	}
}

// attachUserToRoom hooks every active connection of userID into roomID's
// hub so they start receiving live broadcasts. Idempotent — calling for
// a user who is already attached just re-uses the existing entry.
func attachUserToRoom(userID, roomID string) {
	clientRegistry.forUser(userID, func(c *Client) {
		c.hubsMu.Lock()
		_, already := c.hubs[roomID]
		c.hubsMu.Unlock()
		if already {
			return
		}
		h := manager.attachClient(roomID, c)
		c.hubsMu.Lock()
		c.hubs[roomID] = h
		c.hubsMu.Unlock()
	})
}

// detachUserFromRoom unhooks every active connection of userID from the
// room's hub. Used when a user leaves a room.
func detachUserFromRoom(userID, roomID string) {
	clientRegistry.forUser(userID, func(c *Client) {
		c.hubsMu.Lock()
		h := c.hubs[roomID]
		delete(c.hubs, roomID)
		c.hubsMu.Unlock()
		if h == nil {
			return
		}
		select {
		case h.unregister <- c:
		case <-h.done:
		}
	})
}

// updateUserDisplayName refreshes the cached display name on every active
// connection of userID. Subsequent outgoing messages from those clients
// will carry the new name.
func updateUserDisplayName(userID, name string) {
	clientRegistry.forUser(userID, func(c *Client) {
		c.hubsMu.Lock()
		c.displayName = name
		c.hubsMu.Unlock()
	})
}

// ─── high-level event emitters ──────────────────────────────────

func emitRoomCreated(creatorID string, room Room) {
	attachUserToRoom(creatorID, room.ID)
	if data, err := json.Marshal(wsRoomCreatedEvent{Type: WSTypeRoomCreated, Room: room}); err == nil {
		notifyUser(creatorID, data)
	}
}

func emitMemberJoin(roomID, joinerID string) {
	user, err := getUser(joinerID)
	if err != nil {
		log.Printf("emitMemberJoin: %v", err)
		return
	}
	// Make sure the joiner's existing connections start receiving the
	// room's broadcasts, then push room_created to surface the new entry
	// in their sidebars.
	attachUserToRoom(joinerID, roomID)
	if room, err := getRoom(roomID); err == nil {
		if data, err := json.Marshal(wsRoomCreatedEvent{Type: WSTypeRoomCreated, Room: room}); err == nil {
			notifyUser(joinerID, data)
		}
	}

	member := Member{
		UserID:      user.ID,
		DisplayName: user.DisplayName,
		Role:        RoleMember,
		JoinedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if data, err := json.Marshal(wsMemberJoinEvent{
		Type: WSTypeMemberJoin, RoomID: roomID, Member: member,
	}); err == nil {
		notifyRoom(roomID, data)
	}
}

func emitMemberLeave(roomID, leaverID string) {
	if data, err := json.Marshal(wsMemberLeaveEvent{
		Type: WSTypeMemberLeave, RoomID: roomID, UserID: leaverID,
	}); err == nil {
		notifyRoom(roomID, data)
	}
	// Notify the leaver's own clients so they can drop the room from the
	// sidebar without a full reload.
	if data, err := json.Marshal(wsRoomDeletedEvent{Type: WSTypeRoomDeleted, RoomID: roomID}); err == nil {
		notifyUser(leaverID, data)
	}
	detachUserFromRoom(leaverID, roomID)
}

func emitRoomDeleted(roomID string) {
	if data, err := json.Marshal(wsRoomDeletedEvent{Type: WSTypeRoomDeleted, RoomID: roomID}); err == nil {
		notifyRoom(roomID, data)
	}
}

func emitOwnerChanged(roomID, newOwnerID string) {
	if data, err := json.Marshal(wsOwnerChangedEvent{
		Type: WSTypeOwnerChanged, RoomID: roomID, NewOwnerID: newOwnerID,
	}); err == nil {
		notifyRoom(roomID, data)
	}
}

func emitRoomUpdated(room Room) {
	if data, err := json.Marshal(wsRoomUpdatedEvent{Type: WSTypeRoomUpdated, Room: room}); err == nil {
		notifyRoom(room.ID, data)
	}
}

// emitNameChanged delivers a name_changed event to every room the user
// is a member of (i.e., everyone they share a room with). Lobby is
// included since membership is implicit.
func emitNameChanged(userID, name string) {
	updateUserDisplayName(userID, name)

	data, err := json.Marshal(wsNameChangedEvent{
		Type: WSTypeNameChanged, UserID: userID, DisplayName: name,
	})
	if err != nil {
		return
	}

	// Every group room the user is a member of, plus the lobby.
	rows, err := db.Query(
		`SELECT room_id FROM room_members WHERE user_id = ?`, userID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var roomID string
		if err := rows.Scan(&roomID); err != nil {
			continue
		}
		notifyRoom(roomID, data)
	}
	notifyRoom(LobbyRoomID, data)
}

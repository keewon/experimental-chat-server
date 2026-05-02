package main

// WS event type tags. Server → client unless marked.
const (
	WSTypeSnapshot     = "snapshot"
	WSTypeMsg          = "msg"           // server↔client
	WSTypeError        = "error"
	WSTypeMemberJoin   = "member_join"
	WSTypeMemberLeave  = "member_leave"
	WSTypeOwnerChanged = "owner_changed"
	WSTypeRoomCreated  = "room_created"
	WSTypeRoomDeleted  = "room_deleted"
	WSTypeRoomUpdated  = "room_updated"
	WSTypeNameChanged  = "name_changed"
)

// ─── server → client ────────────────────────────────────────────

type wsSnapshot struct {
	Type  string        `json:"type"` // WSTypeSnapshot
	Me    User          `json:"me"`
	Rooms []wsRoomEntry `json:"rooms"`
}

type wsRoomEntry struct {
	Room        Room     `json:"room"`
	MyRole      string   `json:"my_role"`
	Hidden      bool     `json:"hidden"`
	LastMessage *Message `json:"last_message,omitempty"`
}

type wsMsgEvent struct {
	Type        string `json:"type"` // WSTypeMsg
	RoomID      string `json:"room_id"`
	ID          int64  `json:"id"`
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Content     string `json:"content"`
	CreatedAt   string `json:"created_at"`
}

type wsErrorEvent struct {
	Type    string `json:"type"` // WSTypeError
	Code    string `json:"code"`
	Message string `json:"message"`
}

type wsMemberJoinEvent struct {
	Type   string `json:"type"` // WSTypeMemberJoin
	RoomID string `json:"room_id"`
	Member Member `json:"member"`
}

type wsMemberLeaveEvent struct {
	Type   string `json:"type"` // WSTypeMemberLeave
	RoomID string `json:"room_id"`
	UserID string `json:"user_id"`
}

type wsOwnerChangedEvent struct {
	Type       string `json:"type"` // WSTypeOwnerChanged
	RoomID     string `json:"room_id"`
	NewOwnerID string `json:"new_owner_id"`
}

type wsRoomCreatedEvent struct {
	Type string `json:"type"` // WSTypeRoomCreated
	Room Room   `json:"room"`
}

type wsRoomDeletedEvent struct {
	Type   string `json:"type"` // WSTypeRoomDeleted
	RoomID string `json:"room_id"`
}

type wsRoomUpdatedEvent struct {
	Type string `json:"type"` // WSTypeRoomUpdated
	Room Room   `json:"room"`
}

type wsNameChangedEvent struct {
	Type        string `json:"type"` // WSTypeNameChanged
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// ─── client → server ────────────────────────────────────────────

type wsIncoming struct {
	Type    string `json:"type"`
	RoomID  string `json:"room_id"`
	Content string `json:"content"`
}

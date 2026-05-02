package main

import "sync"

// ClientRegistry tracks every active WebSocket Client by userID.
//
// Membership/profile changes that come in via REST need to reach the
// user's *active connections* — possibly several, one per device/tab —
// so the lobby and other already-loaded rooms stay live without a manual
// refresh. The REST handlers walk the registry to attach hubs, push
// frames, and update cached display names on the fly.
type ClientRegistry struct {
	mu   sync.Mutex
	sets map[string]map[*Client]struct{}
}

func newClientRegistry() *ClientRegistry {
	return &ClientRegistry{sets: make(map[string]map[*Client]struct{})}
}

func (r *ClientRegistry) add(c *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.sets[c.userID]
	if !ok {
		set = make(map[*Client]struct{})
		r.sets[c.userID] = set
	}
	set[c] = struct{}{}
}

func (r *ClientRegistry) remove(c *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.sets[c.userID]
	if !ok {
		return
	}
	delete(set, c)
	if len(set) == 0 {
		delete(r.sets, c.userID)
	}
}

// forUser invokes fn for each currently-registered client of userID.
// fn runs OUTSIDE the registry lock to avoid deadlocks when fn touches
// hubs (which may take their own locks). We snapshot the set so concurrent
// add/remove during iteration is safe.
func (r *ClientRegistry) forUser(userID string, fn func(*Client)) {
	r.mu.Lock()
	set := r.sets[userID]
	snap := make([]*Client, 0, len(set))
	for c := range set {
		snap = append(snap, c)
	}
	r.mu.Unlock()
	for _, c := range snap {
		fn(c)
	}
}

var clientRegistry = newClientRegistry()

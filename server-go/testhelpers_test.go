package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// setupTestDB opens a fresh in-memory SQLite instance, creates the schema,
// installs it as the global `db`, and registers cleanup. Each test gets
// its own DB so they cannot leak state into each other.
//
// Note: ":memory:" alone gives every connection a distinct DB. We pin
// MaxOpenConns to 1 (matching the production sqlite path) so all queries
// go through the same connection.
func setupTestDB(t *testing.T) {
	t.Helper()
	prev := db
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	db = conn
	if err := createSQLiteTables(); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		db = prev
	})
}

// setupSessionSecret installs a deterministic 32-char secret for tests so
// signCookie/verifyCookie behave repeatably. Restores any previous value.
func setupSessionSecret(t *testing.T) {
	t.Helper()
	prev := sessionSecret
	sessionSecret = []byte(strings.Repeat("k", 32))
	t.Cleanup(func() { sessionSecret = prev })
}

// setupManager replaces the global hub manager with a fresh one whose
// idle teardown timer fires quickly enough for tests. Pass 0 to keep the
// default (1h) — useful when a test does not exercise idle teardown.
func setupManager(t *testing.T, idle time.Duration) {
	t.Helper()
	prev := manager
	m := newHubManager()
	if idle > 0 {
		m.idleTime = idle
	}
	manager = m
	t.Cleanup(func() { manager = prev })
}

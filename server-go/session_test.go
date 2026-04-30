package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignVerifyRoundtrip(t *testing.T) {
	setupSessionSecret(t)

	userID := "user-abc"
	exp := time.Now().Add(time.Hour)
	cookie := signCookie(userID, exp)

	got, ok := verifyCookie(cookie)
	if !ok {
		t.Fatalf("verifyCookie failed for valid cookie")
	}
	if got != userID {
		t.Fatalf("verifyCookie returned %q, want %q", got, userID)
	}
}

func TestVerifyCookieRejectsTamperedSignature(t *testing.T) {
	setupSessionSecret(t)
	cookie := signCookie("user-abc", time.Now().Add(time.Hour))

	parts := strings.Split(cookie, "|")
	if len(parts) != 3 {
		t.Fatalf("unexpected cookie format: %q", cookie)
	}
	// Flip one hex char in the signature.
	sig := []byte(parts[2])
	if sig[0] == '0' {
		sig[0] = '1'
	} else {
		sig[0] = '0'
	}
	tampered := parts[0] + "|" + parts[1] + "|" + string(sig)

	if _, ok := verifyCookie(tampered); ok {
		t.Fatalf("tampered signature accepted")
	}
}

func TestVerifyCookieRejectsExpired(t *testing.T) {
	setupSessionSecret(t)
	cookie := signCookie("user-abc", time.Now().Add(-time.Second))

	if _, ok := verifyCookie(cookie); ok {
		t.Fatalf("expired cookie accepted")
	}
}

func TestVerifyCookieRejectsMalformed(t *testing.T) {
	setupSessionSecret(t)

	for _, bad := range []string{
		"",
		"only-one-part",
		"two|parts",
		"too|many|parts|here",
		"user|not-a-number|deadbeef",
	} {
		if _, ok := verifyCookie(bad); ok {
			t.Errorf("malformed cookie accepted: %q", bad)
		}
	}
}

func TestVerifyCookieRejectsForeignSecret(t *testing.T) {
	// Sign with one secret, verify with another → must reject.
	setupSessionSecret(t)
	cookie := signCookie("user-abc", time.Now().Add(time.Hour))

	prev := sessionSecret
	sessionSecret = []byte(strings.Repeat("z", 32))
	defer func() { sessionSecret = prev }()

	if _, ok := verifyCookie(cookie); ok {
		t.Fatalf("cookie verified under wrong secret")
	}
}

func TestGetUserIDFromRequest(t *testing.T) {
	setupSessionSecret(t)

	t.Run("no cookie", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		if _, ok := getUserIDFromRequest(r); ok {
			t.Fatalf("expected ok=false when no cookie")
		}
	})

	t.Run("valid cookie", func(t *testing.T) {
		userID := "user-xyz"
		val := signCookie(userID, time.Now().Add(time.Hour))
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: val})

		got, ok := getUserIDFromRequest(r)
		if !ok || got != userID {
			t.Fatalf("got=(%q,%v) want=(%q,true)", got, ok, userID)
		}
	})
}

func TestHandleSession_IssuesCookieWhenAbsent(t *testing.T) {
	setupSessionSecret(t)

	r := httptest.NewRequest("GET", "/api/session", nil)
	w := httptest.NewRecorder()
	handleSession(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}

	var body struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID == "" {
		t.Fatalf("user_id missing from response")
	}

	// A Set-Cookie header for the session must be present.
	resp := w.Result()
	defer resp.Body.Close()
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("session cookie not set")
	}
	if !sessionCookie.HttpOnly {
		t.Errorf("cookie should be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite=%v, want Lax", sessionCookie.SameSite)
	}

	// The user_id in the body must match the userID encoded in the cookie.
	encoded, ok := verifyCookie(sessionCookie.Value)
	if !ok {
		t.Fatalf("issued cookie does not verify")
	}
	if encoded != body.UserID {
		t.Fatalf("cookie userID=%q, body=%q", encoded, body.UserID)
	}
}

func TestHandleSession_PreservesExistingCookie(t *testing.T) {
	setupSessionSecret(t)

	userID := "stable-user"
	val := signCookie(userID, time.Now().Add(time.Hour))

	r := httptest.NewRequest("GET", "/api/session", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: val})
	w := httptest.NewRecorder()
	handleSession(w, r)

	var body struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID != userID {
		t.Fatalf("user_id=%q, want %q (existing cookie should be reused)", body.UserID, userID)
	}

	// Should not re-issue when one is already valid.
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			t.Fatalf("session cookie was re-issued unexpectedly")
		}
	}
}

func TestRequireUserID_RejectsMissing(t *testing.T) {
	setupSessionSecret(t)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	id, ok := requireUserID(w, r)
	if ok || id != "" {
		t.Fatalf("requireUserID = (%q,%v), want (\"\", false)", id, ok)
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", w.Code)
	}
}

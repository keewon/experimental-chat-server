package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoCacheMiddleware_SetsHeader(t *testing.T) {
	wrapped := noCacheMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hi"))
	}))

	r := httptest.NewRequest("GET", "/static/x.js", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)

	if got := w.Header().Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if w.Body.String() != "hi" {
		t.Fatalf("body=%q", w.Body.String())
	}
}

func TestAccessLogMiddleware_LogsStatusAndBytes(t *testing.T) {
	// Capture log output for inspection.
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	handler := accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("hello"))
	}))

	r := httptest.NewRequest("GET", "/test?x=1", nil)
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	line := buf.String()
	for _, want := range []string{"[http]", "GET", "/test?x=1", "418", "5B", "ip=9.9.9.9"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q missing %q", line, want)
		}
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(r *http.Request)
		want    string
	}{
		{
			name: "CF-Connecting-IP wins",
			setup: func(r *http.Request) {
				r.Header.Set("CF-Connecting-IP", "1.2.3.4")
				r.Header.Set("X-Forwarded-For", "5.6.7.8")
				r.RemoteAddr = "127.0.0.1:1234"
			},
			want: "1.2.3.4",
		},
		{
			name: "X-Forwarded-For first hop",
			setup: func(r *http.Request) {
				r.Header.Set("X-Forwarded-For", "5.6.7.8, 10.0.0.1, 10.0.0.2")
				r.RemoteAddr = "127.0.0.1:1234"
			},
			want: "5.6.7.8",
		},
		{
			name: "X-Forwarded-For single value",
			setup: func(r *http.Request) {
				r.Header.Set("X-Forwarded-For", "  5.6.7.8  ")
				r.RemoteAddr = "127.0.0.1:1234"
			},
			want: "5.6.7.8",
		},
		{
			name: "RemoteAddr fallback",
			setup: func(r *http.Request) {
				r.RemoteAddr = "192.168.0.1:9999"
			},
			want: "192.168.0.1",
		},
		{
			name: "RemoteAddr without port",
			setup: func(r *http.Request) {
				r.RemoteAddr = "192.168.0.1"
			},
			want: "192.168.0.1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Del("X-Forwarded-For") // httptest sometimes sets these
			r.RemoteAddr = ""
			tc.setup(r)
			if got := clientIP(r); got != tc.want {
				t.Fatalf("clientIP=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckOrigin(t *testing.T) {
	t.Run("empty origin rejected", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "chat.example.com"
		if checkOrigin(r) {
			t.Fatal("empty origin should be rejected")
		}
	})

	t.Run("same host accepted", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "chat.example.com"
		r.Header.Set("Origin", "https://chat.example.com")
		if !checkOrigin(r) {
			t.Fatal("same-host origin should be accepted")
		}
	})

	t.Run("PUBLIC_ORIGIN accepted", func(t *testing.T) {
		t.Setenv("PUBLIC_ORIGIN", "https://chat.acidblob.com")
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "127.0.0.1:8081" // different from PUBLIC_ORIGIN
		r.Header.Set("Origin", "https://chat.acidblob.com")
		if !checkOrigin(r) {
			t.Fatal("PUBLIC_ORIGIN should be accepted")
		}
	})

	t.Run("foreign origin rejected", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "chat.example.com"
		r.Header.Set("Origin", "https://attacker.example.com")
		if checkOrigin(r) {
			t.Fatal("foreign origin should be rejected")
		}
	})
}

func TestIsTruthyEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"yes", true},
		{"on", true},
		{"  yes  ", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"random", false},
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("TEST_FLAG", tc.val)
			if got := isTruthyEnv("TEST_FLAG"); got != tc.want {
				t.Fatalf("isTruthyEnv(%q)=%v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestShortUserID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"90c9c321-9ca0-41b4-92a5-d110e42abf90", "90c9c321"},
		{"abcdefgh", "abcdefgh"},
		{"abc", "abc"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := shortUserID(tc.in); got != tc.want {
			t.Errorf("shortUserID(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

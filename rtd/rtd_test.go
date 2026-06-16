package rtd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent")
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.Rate = 0 // no pacing in the test

	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q, want %q", body, `{"ok":true}`)
	}
}

func TestGetRetriesOn503(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"recovered":true}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.Rate = 0
	c.Retries = 5

	start := time.Now()
	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"recovered":true}` {
		t.Errorf("body = %q after retries", body)
	}
	if hits != 3 {
		t.Errorf("server saw %d hits, want 3", hits)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Error("retries did not back off")
	}
}

func TestNewClientDefaults(t *testing.T) {
	c := NewClient()
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.UserAgent == "" {
		t.Error("UserAgent is empty")
	}
	if c.Rate <= 0 {
		t.Error("Rate should be > 0")
	}
	if c.Retries <= 0 {
		t.Error("Retries should be > 0")
	}
}

func TestGetUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient()
	c.Rate = 0
	c.Retries = 0

	_, err := c.Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}

func TestGetNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient()
	c.Rate = 0
	c.Retries = 0

	_, err := c.Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestTokenInHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.Rate = 0
	c.Token = "mytoken123"

	_, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Token mytoken123" {
		t.Errorf("Authorization = %q, want Token mytoken123", gotAuth)
	}
}

func TestErrConstants(t *testing.T) {
	if ErrNotFound == nil {
		t.Error("ErrNotFound is nil")
	}
	if ErrUnauthorized == nil {
		t.Error("ErrUnauthorized is nil")
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := NewClient()
	c.Rate = 0
	c.Retries = 0

	_, err := c.Get(ctx, "http://127.0.0.1:9")
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

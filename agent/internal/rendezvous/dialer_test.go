package rendezvous

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spadeide/spade-agent/internal/identity"
)

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		raw     string
		wantErr bool
	}{
		{"", false},
		{"  ", false},
		{"wss://rv.example", false},
		{"ws://127.0.0.1:9", false},
		{"https://rv.example", true},
		{"http://rv.example", true},
		{"ftp://rv.example", true},
		{"not-a-url", true},
		{"://broken", true},
	}
	for _, tc := range tests {
		err := ValidateServerURL(tc.raw)
		if tc.wantErr && err == nil {
			t.Errorf("%q: want error", tc.raw)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%q: unexpected error: %v", tc.raw, err)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "45", 45 * time.Second},
		{"zero", "0", 0},
		{"negative", "-3", 0},
		{"garbage", "nope", 0},
		{"cap_24h", "86400", maxRetryAfter},
		{"just_under_cap", "899", 899 * time.Second},
		{"at_cap", "900", maxRetryAfter},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryAfter(tc.raw)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}

	t.Run("http_date_future", func(t *testing.T) {
		future := time.Now().UTC().Add(2 * time.Minute).Format(http.TimeFormat)
		got := parseRetryAfter(future)
		if got < time.Minute || got > maxRetryAfter {
			t.Fatalf("got %v, want ~2m capped at max", got)
		}
	})

	t.Run("http_date_past", func(t *testing.T) {
		past := time.Now().UTC().Add(-time.Minute).Format(http.TimeFormat)
		if got := parseRetryAfter(past); got != 0 {
			t.Fatalf("got %v want 0", got)
		}
	})

	t.Run("http_date_far_capped", func(t *testing.T) {
		far := time.Now().UTC().Add(24 * time.Hour).Format(http.TimeFormat)
		if got := parseRetryAfter(far); got != maxRetryAfter {
			t.Fatalf("got %v want %v", got, maxRetryAfter)
		}
	})
}

func TestDialWSError429(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"60"}},
		Body:       io.NopCloser(strings.NewReader("slow down")),
	}
	err := dialWSError("dial", resp, errors.New("websocket: bad handshake"))
	var ra *retryAfterError
	if !errors.As(err, &ra) {
		t.Fatalf("want retryAfterError, got %T %v", err, err)
	}
	if ra.after != 60*time.Second {
		t.Fatalf("after=%v want 60s", ra.after)
	}

	resp = &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"86400"}},
		Body:       io.NopCloser(strings.NewReader("")),
	}
	err = dialWSError("dial", resp, errors.New("websocket: bad handshake"))
	if !errors.As(err, &ra) {
		t.Fatalf("want retryAfterError, got %v", err)
	}
	if ra.after != maxRetryAfter {
		t.Fatalf("after=%v want capped %v", ra.after, maxRetryAfter)
	}

	resp = &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}
	err = dialWSError("dial", resp, errors.New("websocket: bad handshake"))
	if errors.As(err, &ra) {
		t.Fatal("non-429 must not be retryAfterError")
	}
}

func TestApplyGenerationKeepsRegistered(t *testing.T) {
	srv := stubRendezvous(t)
	defer srv.Close()

	id, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &Dialer{Identity: id}
	base := "ws://" + strings.TrimPrefix(srv.URL, "http://")

	d.Apply(true, base)
	waitRegistered(t, d, true)

	d.Apply(true, base)
	waitRegistered(t, d, true)

	// Give the cancelled gen-1 session time to run its defer.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_, _, reg := d.State()
		if !reg {
			t.Fatal("stale session defer cleared registered after second Apply")
		}
		time.Sleep(20 * time.Millisecond)
	}

	d.Apply(false, "")
	waitRegistered(t, d, false)
}

func stubRendezvous(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent" {
			http.NotFound(w, r)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var hello envelope
		if err := c.ReadJSON(&hello); err != nil {
			return
		}
		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)
		if err := c.WriteJSON(envelope{
			"t":     "challenge",
			"nonce": base64.StdEncoding.EncodeToString(nonce),
		}); err != nil {
			return
		}
		var prove envelope
		if err := c.ReadJSON(&prove); err != nil {
			return
		}
		if err := c.WriteJSON(envelope{"t": "ready"}); err != nil {
			return
		}
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
}

func waitRegistered(t *testing.T, d *Dialer, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, _, reg := d.State()
		if reg == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, _, reg := d.State()
	t.Fatalf("registered=%v want %v", reg, want)
}

package pty

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spadeide/spade-agent/internal/protocol"
)

type ptyOutput struct {
	mu   sync.Mutex
	data strings.Builder
}

func (o *ptyOutput) appendChunk(b64 string) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return
	}
	o.mu.Lock()
	o.data.Write(raw)
	o.mu.Unlock()
}

func (o *ptyOutput) waitContains(substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		ok := strings.Contains(o.data.String(), substr)
		o.mu.Unlock()
		if ok {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func newTestManager(t *testing.T, shell string) (*Manager, *ptyOutput) {
	t.Helper()
	out := &ptyOutput{}
	m := NewManager(shell, func(env protocol.Envelope) {
		if env.Event != protocol.EventPTYData {
			return
		}
		var p protocol.PTYDataParams
		if err := json.Unmarshal(env.Params, &p); err != nil {
			return
		}
		out.appendChunk(p.Data)
	})
	t.Cleanup(func() { m.CloseAll() })
	return m, out
}

func TestManagerCreateInputResizeClose(t *testing.T) {
	t.Parallel()
	m, out := newTestManager(t, "/bin/sh")

	id, err := m.Create(80, 24, t.TempDir())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mark := "SPADE_PTY_MARK_7\n"
	if err := m.Input(id, base64.StdEncoding.EncodeToString([]byte(mark))); err != nil {
		t.Fatalf("Input: %v", err)
	}
	if !out.waitContains("SPADE_PTY_MARK_7", 3*time.Second) {
		t.Fatal("expected shell output containing mark")
	}

	if err := m.Resize(id, 120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := m.Close(id); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Input(id, base64.StdEncoding.EncodeToString([]byte("x"))); err == nil {
		t.Fatal("Input after close should fail")
	}
}

func TestReadLoopCoalescesBulkOutput(t *testing.T) {
	// 100 KiB of yes-output under the old 4 KiB path ≈ 25+ envelopes.
	// With 32 KiB + 5 ms coalesce we expect far fewer.
	const nbytes = 100_000
	var events atomic.Int32
	var bytes atomic.Int64
	m := NewManager("/bin/sh", func(env protocol.Envelope) {
		if env.Event != protocol.EventPTYData {
			return
		}
		var p protocol.PTYDataParams
		if err := json.Unmarshal(env.Params, &p); err != nil {
			return
		}
		raw, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return
		}
		events.Add(1)
		bytes.Add(int64(len(raw)))
	})
	t.Cleanup(func() { m.CloseAll() })

	id, err := m.Create(80, 24, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := "yes | head -c 100000; echo SPADE_PTY_DONE\n"
	if err := m.Input(id, base64.StdEncoding.EncodeToString([]byte(cmd))); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Load() >= nbytes {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	gotBytes := bytes.Load()
	gotEvents := events.Load()
	if gotBytes < nbytes {
		t.Fatalf("bytes=%d want >= %d events=%d", gotBytes, nbytes, gotEvents)
	}
	// Old path ≈ bytes/4096; require clearly under half of that.
	oldApprox := int32(gotBytes/4096) + 1
	if gotEvents >= oldApprox/2 {
		t.Fatalf("events=%d not coalesced enough (old≈%d for %d bytes)", gotEvents, oldApprox, gotBytes)
	}
}

func TestManagerInvalidInputAndMissingPTY(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager(t, "/bin/sh")

	if err := m.Input("missing", "!!!"); err == nil {
		t.Fatal("expected invalid base64 error")
	}
	if err := m.Input("missing", base64.StdEncoding.EncodeToString([]byte("x"))); err == nil {
		t.Fatal("expected pty not found")
	}
	if err := m.Resize("missing", 80, 24); err == nil {
		t.Fatal("expected pty not found on resize")
	}
	if err := m.Close("missing"); err != nil {
		t.Fatalf("Close missing should be nil, got %v", err)
	}
}

func TestManagerMaxSessionsEnforced(t *testing.T) {
	m, _ := newTestManager(t, "/bin/sh")
	ids := make([]string, 0, MaxSessions)
	for i := 0; i < MaxSessions; i++ {
		id, err := m.Create(80, 24, "")
		if err != nil {
			t.Fatalf("Create %d/%d: %v", i+1, MaxSessions, err)
		}
		ids = append(ids, id)
	}
	if _, err := m.Create(80, 24, ""); err == nil {
		t.Fatal("expected cap error on session MaxSessions+1")
	} else if !strings.Contains(err.Error(), "too many terminals") {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range ids {
		_ = m.Close(id)
	}
	id, err := m.Create(80, 24, "")
	if err != nil {
		t.Fatalf("Create after freeing slot: %v", err)
	}
	_ = m.Close(id)
}

func TestManagerCloseKillsProcessGroup(t *testing.T) {
	m, out := newTestManager(t, "/bin/sh")
	id, err := m.Create(80, 24, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Avoid matching local-echo of the typed "$!" — wait for a numeric pid.
	script := "sleep 3600 & echo PTYCHILD:$!\n"
	if err := m.Input(id, base64.StdEncoding.EncodeToString([]byte(script))); err != nil {
		t.Fatalf("Input: %v", err)
	}
	pid := 0
	deadlineMark := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadlineMark) {
		out.mu.Lock()
		raw := out.data.String()
		out.mu.Unlock()
		if p := parsePIDAfterMarker(raw, "PTYCHILD:"); p > 0 {
			pid = p
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("timeout waiting for child pid marker")
	}

	if err := m.Close(id); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child pid %d still alive after session close", pid)
}

func parsePIDAfterMarker(raw, marker string) int {
	for {
		i := strings.Index(raw, marker)
		if i < 0 {
			return 0
		}
		rest := raw[i+len(marker):]
		var pid int
		for _, c := range rest {
			if c < '0' || c > '9' {
				break
			}
			pid = pid*10 + int(c-'0')
		}
		if pid > 0 {
			return pid
		}
		raw = rest
	}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

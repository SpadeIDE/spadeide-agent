package server

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/hostauth"
	"github.com/spadeide/spade-agent/internal/protocol"
	ptypkg "github.com/spadeide/spade-agent/internal/pty"
)

// TestReplaceFSConcurrentWithFilesystemAccess stresses the A1 race: handlers
// reading c.fs / c.unixUser while host.login swaps them. Under -race this must
// stay clean.
func TestReplaceFSConcurrentWithFilesystemAccess(t *testing.T) {
	home := t.TempDir()
	alt := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alt, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Server{}
	c := &client{
		device: &config.Device{ID: "dev-race", Name: "race"},
		closed: make(chan struct{}),
	}
	s.attachUserHome(c, home)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if fs := c.filesystem(); fs != nil {
				_, _ = fs.List(".")
			}
			_ = c.boundUser()
			_ = c.pairedDevice()
			_ = c.requireBound(protocol.Envelope{Method: protocol.MethodFSList, ID: "x"})
			if pty := c.terminal(); pty != nil {
				_ = pty.LoginShell()
			}
		}
	}()

	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		for i := 0; i < 300; i++ {
			if i%2 == 0 {
				s.replaceFS(c, home, false, 0, 0)
				s.replacePTY(c, "/bin/sh", ptypkg.Identity{Home: home, Shell: "/bin/sh"})
				c.setBoundUser("user-a")
			} else {
				s.replaceFS(c, alt, false, 0, 0)
				s.replacePTY(c, "/bin/bash", ptypkg.Identity{Home: alt, Shell: "/bin/bash"})
				c.setBoundUser("user-b")
			}
		}
	}()
	go func() {
		defer writers.Done()
		for i := 0; i < 150; i++ {
			s.attachAccount(c, &hostauth.Account{
				Name:  "u",
				UID:   501,
				GID:   20,
				Home:  home,
				Shell: "/bin/sh",
			})
			s.attachUserHome(c, alt)
		}
	}()

	writers.Wait()
	close(stop)
	readers.Wait()
	c.closeSessionIO()
}

// TestTeardownDuringInFlightHandler is the A1 crash regression: closeSessionIO
// must not nil fs/pty while an RPC goroutine is still using them.
func TestTeardownDuringInFlightHandler(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 50; i++ {
		name := filepath.Join(home, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{}
	c := &client{
		device: &config.Device{ID: "dev-teardown", Name: "t"},
		closed: make(chan struct{}),
	}
	s.attachUserHome(c, home)

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			fs := c.filesystem()
			if fs == nil {
				t.Error("filesystem() returned nil during/after teardown")
				return
			}
			_, _ = fs.List(".")
			_ = c.terminal()
		}
	}()
	close(start)
	for i := 0; i < 20; i++ {
		c.closeSessionIO()
	}
	wg.Wait()
	if c.filesystem() == nil || c.terminal() == nil {
		t.Fatal("closeSessionIO nilled session pointers")
	}
}

type frameSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (f *frameSink) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}

func TestDispatchInboundRecoverReplies(t *testing.T) {
	sink := &frameSink{}
	c := &client{
		rpc:    sink,
		closed: make(chan struct{}),
	}
	testHookPanicMethod = "test.panic"
	t.Cleanup(func() { testHookPanicMethod = "" })

	c.dispatchInbound(protocol.Envelope{ID: "req-1", Method: "test.panic"}, sink)

	deadline := time.Now().Add(2 * time.Second)
	for {
		sink.mu.Lock()
		raw := append([]byte(nil), sink.buf.Bytes()...)
		sink.mu.Unlock()
		if len(raw) > 0 {
			env, err := readFrame(bytes.NewReader(raw))
			if err == nil {
				if env.ID != "req-1" || env.Error == nil || env.Error.Code != protocol.CodeInternal {
					t.Fatalf("got %+v", env)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no error reply after panic")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

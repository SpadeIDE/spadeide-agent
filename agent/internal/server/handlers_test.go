package server

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
	gitpkg "github.com/spadeide/spade-agent/internal/git"
	"github.com/spadeide/spade-agent/internal/protocol"
)

func TestHandlerPingAndHello(t *testing.T) {
	home := t.TempDir()
	c := testClient(t, home)

	c.handle(protocol.Envelope{ID: "p", Method: protocol.MethodPing})
	env := mustReadReply(t, c)
	if env.ID != "p" || env.OK == nil || !*env.OK {
		t.Fatalf("ping: %+v", env)
	}

	c.handle(protocol.Envelope{ID: "h", Method: protocol.MethodHello})
	env = mustReadReply(t, c)
	if env.ID != "h" || env.OK == nil || !*env.OK {
		t.Fatalf("hello: %+v", env)
	}
	var hello protocol.HelloResult
	if err := json.Unmarshal(env.Result, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Home != home {
		t.Fatalf("home=%q want %q", hello.Home, home)
	}
}

func TestHandlerUnknownMethod(t *testing.T) {
	c := testClient(t, t.TempDir())
	c.handle(protocol.Envelope{ID: "u", Method: "no.such"})
	env := mustReadReply(t, c)
	if env.Error == nil || env.Error.Code != protocol.CodeNotFound {
		t.Fatalf("got %+v", env)
	}
}

func TestHandlersMalformedParams(t *testing.T) {
	// Methods that decode params — broken JSON must yield invalid_params.
	needParams := []string{
		protocol.MethodWorkspaceOpen,
		protocol.MethodFSList,
		protocol.MethodFSStat,
		protocol.MethodFSRead,
		protocol.MethodFSMkdir,
		protocol.MethodFSDelete,
		protocol.MethodFSWatch,
		protocol.MethodFSWrite,
		protocol.MethodFSRename,
		protocol.MethodFSCopy,
		protocol.MethodFSMove,
		protocol.MethodFSSearch,
		protocol.MethodFSGrep,
		protocol.MethodFSReplaceInFiles,
		protocol.MethodFSUnwatch,
		protocol.MethodFSExtract,
		protocol.MethodFSArchive,
		protocol.MethodGitStage,
		protocol.MethodGitUnstage,
		protocol.MethodGitDiscard,
		protocol.MethodGitCommit,
		protocol.MethodGitPush,
		protocol.MethodGitPull,
		protocol.MethodGitDiff,
		protocol.MethodGitApplyHunk,
		protocol.MethodGitCheckout,
		protocol.MethodPTYCreate,
		protocol.MethodPTYInput,
		protocol.MethodPTYResize,
		protocol.MethodPTYClose,
	}
	for _, method := range needParams {
		if _, ok := handlers[method]; !ok {
			t.Fatalf("handlers missing %s", method)
		}
	}

	home := t.TempDir()
	c := testClient(t, home)
	bad := json.RawMessage(`{`)
	for _, method := range needParams {
		t.Run(method, func(t *testing.T) {
			clearSink(c)
			c.handle(protocol.Envelope{ID: "bad", Method: method, Params: bad})
			env := mustReadReply(t, c)
			if env.Error == nil || env.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("got %+v", env)
			}
		})
	}
}

func TestHandlerFSSandbox(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "ok.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	c := testClient(t, home)

	params, _ := json.Marshal(protocol.PathParams{Path: home})
	c.handle(protocol.Envelope{ID: "open", Method: protocol.MethodWorkspaceOpen, Params: params})
	env := mustReadReply(t, c)
	if env.OK == nil || !*env.OK {
		t.Fatalf("open: %+v", env)
	}

	params, _ = json.Marshal(protocol.PathParams{Path: filepath.Join(outside, "x")})
	c.handle(protocol.Envelope{ID: "list", Method: protocol.MethodFSList, Params: params})
	env = mustReadReply(t, c)
	if env.Error == nil || env.Error.Code != protocol.CodeForbidden {
		t.Fatalf("outside list: %+v", env)
	}

	params, _ = json.Marshal(protocol.PathParams{Path: filepath.Join(home, "ok.txt")})
	c.handle(protocol.Envelope{ID: "stat", Method: protocol.MethodFSStat, Params: params})
	env = mustReadReply(t, c)
	if env.OK == nil || !*env.OK {
		t.Fatalf("stat in home: %+v", env)
	}
}

func TestHandlersTableCoversProtocolMethods(t *testing.T) {
	if len(handlers) != len(protocol.AllMethods) {
		t.Fatalf("handlers=%d AllMethods=%d", len(handlers), len(protocol.AllMethods))
	}
	for _, m := range protocol.AllMethods {
		if _, ok := handlers[m]; !ok {
			t.Fatalf("missing handler for %s (add to handlers map)", m)
		}
	}
	for m := range handlers {
		found := false
		for _, want := range protocol.AllMethods {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("handler %q not in protocol.AllMethods", m)
		}
	}
}

func testClient(t *testing.T, home string) *client {
	t.Helper()
	s := &Server{}
	sink := &frameSink{}
	c := &client{
		control: sink,
		rpc:     sink,
		device:  &config.Device{ID: "dev-handler", Name: "t"},
		git:     gitpkg.New(),
		closed:  make(chan struct{}),
	}
	c.srv = s
	s.attachUserHome(c, home)
	return c
}

func clearSink(c *client) {
	if s, ok := c.rpc.(*frameSink); ok {
		s.mu.Lock()
		s.buf.Reset()
		s.mu.Unlock()
	}
}

func mustReadReply(t *testing.T, c *client) protocol.Envelope {
	t.Helper()
	sink, ok := c.rpc.(*frameSink)
	if !ok {
		t.Fatal("rpc is not frameSink")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		sink.mu.Lock()
		raw := append([]byte(nil), sink.buf.Bytes()...)
		sink.mu.Unlock()
		if len(raw) >= 4 {
			env, err := readFrame(bytes.NewReader(raw))
			if err == nil {
				clearSink(c)
				return env
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no reply (buf=%d)", len(raw))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/spadeide/spade-agent/internal/auth"
	"github.com/spadeide/spade-agent/internal/config"
	"github.com/spadeide/spade-agent/internal/identity"
	"github.com/spadeide/spade-agent/internal/protocol"
)

func TestServeFramedAuthAndPing(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authMgr, err := auth.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spkiDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	spki := base64.StdEncoding.EncodeToString(spkiDER)
	secret, _ := authMgr.PairSecret()
	if _, err := authMgr.ValidateAndPair(secret, "dev-framed", "test", spki); err != nil {
		t.Fatal(err)
	}

	s := New(0, store, authMgr, id)
	srv, cli := net.Pipe()
	_ = cli.SetDeadline(time.Now().Add(5 * time.Second))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.serveFramed(srv, "test")
	}()

	if err := writeFrame(cli, protocol.Envelope{ID: "c1", Method: MethodAuthChallenge}); err != nil {
		t.Fatal(err)
	}
	chal, err := readFrame(cli)
	if err != nil {
		t.Fatal(err)
	}
	if chal.OK == nil || !*chal.OK {
		t.Fatalf("challenge: %+v", chal)
	}
	var body struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(chal.Result, &body); err != nil || body.Nonce == "" {
		t.Fatalf("nonce: %v %q", err, chal.Result)
	}

	ts := time.Now().Unix()
	sum := sha256.Sum256([]byte(auth.AuthMessage(authMgr.Fingerprint(), ts, body.Nonce)))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(authParams{
		DeviceID: "dev-framed",
		TS:       ts,
		Nonce:    body.Nonce,
		Sig:      base64.StdEncoding.EncodeToString(sig),
	})
	if err := writeFrame(cli, protocol.Envelope{ID: "a1", Method: MethodAuth, Params: params}); err != nil {
		t.Fatal(err)
	}
	authOK, err := readFrame(cli)
	if err != nil {
		t.Fatal(err)
	}
	if authOK.OK == nil || !*authOK.OK {
		t.Fatalf("auth: %+v", authOK)
	}

	if err := writeFrame(cli, protocol.Envelope{ID: "p1", Method: protocol.MethodPing}); err != nil {
		t.Fatal(err)
	}
	pong, err := readFrame(cli)
	if err != nil {
		t.Fatal(err)
	}
	if pong.ID != "p1" || pong.OK == nil || !*pong.OK {
		t.Fatalf("ping: %+v", pong)
	}

	_ = cli.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serveFramed did not exit after client close")
	}
}

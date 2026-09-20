package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spadeide/spade-agent/internal/protocol"
)

// Smoke client: pair + hello + list home.
// Usage: go run ./cmd/smoke <secret> <host> <port> <fingerprint>
func main() {
	if len(os.Args) < 5 {
		fmt.Println("usage: smoke <secret> <host> <port> <fingerprint>")
		os.Exit(2)
	}
	secret, host, port, fp := os.Args[1], os.Args[2], os.Args[3], os.Args[4]

	tlsCfg := &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			sum := sha256.Sum256(rawCerts[0])
			got := hex.EncodeToString(sum[:])
			if got != fp {
				return fmt.Errorf("fp mismatch got=%s want=%s", got, fp)
			}
			return nil
		},
	}

	body, _ := json.Marshal(protocol.PairRequest{
		Secret:     secret,
		DeviceName: "smoke",
		DeviceID:   "smoke-device",
	})
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("https://%s:%s/pair", host, port), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}, Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		panic(fmt.Sprintf("pair failed: %s %s", res.Status, data))
	}
	var pair protocol.PairResponse
	if err := json.Unmarshal(data, &pair); err != nil {
		panic(err)
	}
	fmt.Println("paired, token len", len(pair.Token))

	dialer := websocket.Dialer{TLSClientConfig: tlsCfg}
	wsURL := fmt.Sprintf("wss://%s:%s/v1?token=%s", host, port, pair.Token)
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	send := func(method string, params any) {
		raw, _ := json.Marshal(params)
		env := protocol.Envelope{ID: method + "-1", Method: method, Params: raw}
		if err := conn.WriteJSON(env); err != nil {
			panic(err)
		}
		var resp protocol.Envelope
		if err := conn.ReadJSON(&resp); err != nil {
			panic(err)
		}
		b, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(b))
		if resp.OK != nil && !*resp.OK {
			panic(resp.Error.Message)
		}
	}

	send(protocol.MethodHello, protocol.HelloParams{Client: "smoke", Version: "0.1.0"})
	home, _ := os.UserHomeDir()
	send(protocol.MethodWorkspaceOpen, protocol.PathParams{Path: home})
	send(protocol.MethodFSList, protocol.PathParams{Path: home})
	fmt.Println("smoke OK")
}

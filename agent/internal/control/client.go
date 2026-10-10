package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spadeide/spade-agent/internal/config"
)

// Client talks to the loopback control API of a running agent.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func DefaultClient() (*Client, error) {
	port := config.DefaultControlPort
	token := config.EnvControlTokenValue()
	store, err := config.NewStore("")
	if err == nil {
		if data, err := os.ReadFile(store.ControlPortPath()); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n > 0 {
				port = n
			}
		}
		if token == "" {
			token = store.ReadControlToken()
		}
	}
	return &Client{
		Base:  fmt.Sprintf("http://127.0.0.1:%d", port),
		Token: token,
		HTTP:  &http.Client{Timeout: 3 * time.Second},
	}, nil
}

func (c *Client) Status() (*Status, error) {
	return c.get("/v1/status")
}

func (c *Client) RotatePair() (*Status, error) {
	return c.post("/v1/pair/rotate", nil)
}

func (c *Client) ResetPair() (*Status, error) {
	return c.post("/v1/pair/reset", nil)
}

func (c *Client) UnpairDevice(id string) (*Status, error) {
	req, err := http.NewRequest(http.MethodDelete, c.Base+"/v1/pair/devices/"+id, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) SetRemote(enabled bool, server string) (*Status, error) {
	body := map[string]any{"enabled": enabled}
	if server != "" {
		body["server"] = server
	}
	return c.post("/v1/remote", body)
}

// Ready reports whether the control API answers.
func (c *Client) Ready() bool {
	st, err := c.Status()
	return err == nil && st != nil && st.Running
}

func (c *Client) get(path string) (*Status, error) {
	req, err := http.NewRequest(http.MethodGet, c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) post(path string, body any) (*Status, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, c.Base+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req)
}

func (c *Client) do(req *http.Request) (*Status, error) {
	return c.doOnce(req, true)
}

func (c *Client) doOnce(req *http.Request, retryOnUnauthorized bool) (*Status, error) {
	c.refreshToken()
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	} else {
		req.Header.Del("Authorization")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized && retryOnUnauthorized {
		prev := c.Token
		c.Token = ""
		c.refreshToken()
		if c.Token != "" && c.Token != prev {
			// Rebuild body for POST retries — GET has nil body.
			return c.doOnce(req.Clone(req.Context()), false)
		}
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(data)))
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// refreshToken picks up ~/.spade-agent/control.token written by a just-started agent.
func (c *Client) refreshToken() {
	if v := config.EnvControlTokenValue(); v != "" {
		c.Token = v
		return
	}
	store, err := config.NewStore("")
	if err != nil {
		return
	}
	if tok := store.ReadControlToken(); tok != "" {
		c.Token = tok
	}
	if data, err := os.ReadFile(store.ControlPortPath()); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n > 0 {
			c.Base = fmt.Sprintf("http://127.0.0.1:%d", n)
		}
	}
}

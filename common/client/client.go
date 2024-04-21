package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
)

// simple client for json requests/responses over http over unix socket
type StyxClient struct {
	cli *http.Client
}

func NewClient(addr string) *StyxClient {
	cli := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", addr)
			},
		},
	}
	return &StyxClient{cli: cli}
}

func (c *StyxClient) Call(path string, req, res any) (int, error) {
	u := &url.URL{
		Scheme: "http",
		Host:   "_",
		Path:   path,
	}
	buf, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	httpRes, err := c.cli.Post(u.String(), "application/json", bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	defer httpRes.Body.Close()
	return httpRes.StatusCode, json.NewDecoder(httpRes.Body).Decode(res)
}

func (c *StyxClient) CallAndPrint(path string, req any) error {
	var res any
	status, err := c.Call(path, req, &res)
	if err != nil {
		fmt.Println("call error:", err)
		return err
	}
	if status != http.StatusOK {
		fmt.Println("status:", status)
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

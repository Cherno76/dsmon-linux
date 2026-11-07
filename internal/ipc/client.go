package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

const clientTimeout = 6 * time.Second

type Client struct {
	Path string
}

func NewClient(path string) *Client { return &Client{Path: path} }

func (c *Client) Do(ctx context.Context, request Request) (json.RawMessage, error) {
	if c == nil || c.Path == "" {
		return nil, errors.New("ipc socket path is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.V = Version
	dialer := net.Dialer{Timeout: clientTimeout}
	conn, err := dialer.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return nil, fmt.Errorf("connect daemon: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(clientTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, fmt.Errorf("send ipc request: %w", err)
	}
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return nil, fmt.Errorf("read ipc response: %w", err)
	}
	if response.V != Version {
		return nil, fmt.Errorf("unsupported ipc version %d", response.V)
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "daemon request failed"
		}
		return nil, errors.New(response.Error)
	}
	return response.Data, nil
}

func (c *Client) Stats(ctx context.Context) (Stats, error) {
	var value Stats
	err := c.decode(ctx, Request{Cmd: "stats"}, &value)
	return value, err
}

func (c *Client) Trace(ctx context.Context, width int) (Trace, error) {
	var value Trace
	err := c.decode(ctx, Request{Cmd: "trace", Width: width}, &value)
	return value, err
}

func (c *Client) Series(ctx context.Context, period, dimension string, from, to time.Time) (Series, error) {
	request := Request{Cmd: "series", Range: period, Dimension: dimension}
	if !from.IsZero() {
		request.From = from.Unix()
	}
	if !to.IsZero() {
		request.To = to.Unix()
	}
	var value Series
	err := c.decode(ctx, request, &value)
	return value, err
}

func (c *Client) Requests(ctx context.Context, limit, offset int) (Requests, error) {
	var value Requests
	err := c.decode(ctx, Request{Cmd: "requests", Limit: limit, Offset: offset}, &value)
	return value, err
}

func (c *Client) Pricing(ctx context.Context) (Pricing, error) {
	var value Pricing
	err := c.decode(ctx, Request{Cmd: "pricing"}, &value)
	return value, err
}

// Reload 让 daemon 重新读取配置文件；成功返回时新设置已经生效。
func (c *Client) Reload(ctx context.Context) error {
	_, err := c.Do(ctx, Request{Cmd: "reload"})
	return err
}

func (c *Client) decode(ctx context.Context, request Request, output any) error {
	raw, err := c.Do(ctx, request)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return errors.New("daemon returned an empty response")
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("decode ipc data: %w", err)
	}
	return nil
}

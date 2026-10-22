package esl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Message is one framed event socket message.
type Message struct {
	Headers map[string]string
	Body    string
}

// Type returns the Content-Type.
func (m *Message) Type() string { return m.Headers["Content-Type"] }

// EventHeaders parses a text/event-plain body.
func (m *Message) EventHeaders() map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(m.Body, "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		if dec, err := url.QueryUnescape(v); err == nil {
			v = dec
		}
		out[k] = v
	}
	return out
}

// Client is a minimal event socket client.
type Client struct {
	nc      net.Conn
	r       *bufio.Reader
	replies chan *Message
	events  chan *Message
	logs    chan *Message
	mu      sync.Mutex
	closed  chan struct{}
}

// Dial connects and authenticates.
func Dial(addr, password string) (*Client, error) {
	nc, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{nc: nc, r: bufio.NewReader(nc), replies: make(chan *Message, 16),
		events: make(chan *Message, 1024), logs: make(chan *Message, 4096), closed: make(chan struct{})}
	m, err := c.read()
	if err != nil || m.Type() != "auth/request" {
		nc.Close()
		return nil, fmt.Errorf("unexpected greeting: %v", err)
	}
	if _, err := io.WriteString(nc, "auth "+password+"\n\n"); err != nil {
		nc.Close()
		return nil, err
	}
	m, err = c.read()
	if err != nil {
		nc.Close()
		return nil, err
	}
	if !strings.HasPrefix(m.Headers["Reply-Text"], "+OK") {
		nc.Close()
		return nil, errors.New("authentication failed")
	}
	go c.loop()
	return c, nil
}

func (c *Client) read() (*Message, error) {
	m := &Message{Headers: map[string]string{}}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(m.Headers) == 0 {
				continue
			}
			break
		}
		k, v, _ := strings.Cut(line, ": ")
		m.Headers[k] = v
	}
	if n, err := strconv.Atoi(m.Headers["Content-Length"]); err == nil && n > 0 {
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		m.Body = string(buf)
	}
	return m, nil
}

func (c *Client) loop() {
	defer close(c.closed)
	for {
		m, err := c.read()
		if err != nil {
			return
		}
		switch m.Type() {
		case "text/event-plain", "text/event-json":
			select {
			case c.events <- m:
			default:
			}
		case "log/data":
			select {
			case c.logs <- m:
			default:
			}
		case "text/disconnect-notice":
			return
		default:
			c.replies <- m
		}
	}
}

// Send sends a command and waits for its reply.
func (c *Client) Send(cmd string) (*Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := io.WriteString(c.nc, cmd+"\n\n"); err != nil {
		return nil, err
	}
	select {
	case m := <-c.replies:
		return m, nil
	case <-c.closed:
		return nil, errors.New("connection closed")
	case <-time.After(120 * time.Second):
		return nil, errors.New("timeout waiting for reply")
	}
}

// API runs an api command and returns the response body.
func (c *Client) API(cmd string) (string, error) {
	m, err := c.Send("api " + cmd)
	if err != nil {
		return "", err
	}
	return m.Body, nil
}

// Events returns received events.
func (c *Client) Events() <-chan *Message { return c.events }

// Logs returns received log lines ("log 7").
func (c *Client) Logs() <-chan *Message { return c.logs }

// Closed is closed when the connection ends.
func (c *Client) Closed() <-chan struct{} { return c.closed }

// Close disconnects.
func (c *Client) Close() error { return c.nc.Close() }

package discoverd

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/flynn/flynn/pkg/dialer"
	"github.com/flynn/flynn/pkg/httpclient"
	"github.com/flynn/flynn/pkg/stream"
)

var ErrTimedOut = errors.New("discoverd: timed out waiting for instances")
var PinTTL = 60 * time.Second

type Config struct {
	Endpoints []string
}

type Client struct {
	servers    []*httpclient.Client
	pinned     int
	pinUpdated time.Time
	leader     int
	mu         sync.RWMutex
}

func NewClientWithConfig(config Config) *Client {
	client := &Client{
		servers: make([]*httpclient.Client, 0, len(config.Endpoints)),
	}
	for _, e := range config.Endpoints {
		client.servers = append(client.servers, client.httpClient(e))
	}
	return client
}

func NewClientWithURL(url string) *Client {
	return NewClientWithConfig(Config{Endpoints: formatURLs([]string{url})})
}

func NewClient() *Client {
	return NewClientWithConfig(defaultConfig())
}

func defaultConfig() Config {
	urls := os.Getenv("DISCOVERD")
	if urls == "" || urls == "none" {
		urls = "http://127.0.0.1:1111"
	}
	return Config{Endpoints: formatURLs(strings.Split(urls, ","))}
}

func formatURLs(urls []string) []string {
	formatted := make([]string, 0, len(urls))
	for _, u := range urls {
		if !strings.HasPrefix(u, "http") {
			u = "http://" + u
		}
		formatted = append(formatted, u)
	}
	return formatted
}

func (c *Client) updateLeader(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range c.servers {
		if s.Host == host {
			c.leader = i
		}
	}
}

func (c *Client) httpClient(url string) *httpclient.Client {
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if len(via) > 0 {
			for attr, val := range via[0].Header {
				if _, ok := req.Header[attr]; !ok {
					req.Header[attr] = val
				}
			}
		}
		c.updateLeader(req.Host)
		return nil
	}
	return &httpclient.Client{
		URL: url,
		HTTP: &http.Client{
			Transport:     &http.Transport{Dial: dialer.Retry.Dial},
			CheckRedirect: checkRedirect,
		},
	}
}

func (c *Client) Send(method string, path string, in, out interface{}) (err error) {
	var leaderReq bool
	switch method {
	case "PUT", "DEL", "POST":
		leaderReq = true
	}

	c.mu.RLock()
	leader := c.leader
	pinned := c.pinned
	pinUpdated := c.pinUpdated
	c.mu.RUnlock()

	// try direct writes directly to the leader to avoid redirect
	if leaderReq {
		pinned = leader
	}

	errors := make([]string, 0, len(c.servers))
	for i := pinned; i < len(c.servers)+pinned; i++ {
		k := i % len(c.servers)
		hc := c.servers[k]
		err := hc.Send(method, path, in, out)
		if isNetError(err) {
			errors = append(errors, err.Error())
			continue
		} else if err != nil {
			return err
		}
		if time.Since(pinUpdated) > PinTTL {
			k = 0 // TTL has timed out, use preffered server on next request
		}
		if k != pinned && !leaderReq { // don't update the pin on leader requests
			c.mu.Lock()
			c.pinned = k
			if pinned == 0 {
				// Only restart the TTL if the preferred server was pinned but failed
				c.pinUpdated = time.Now()
			}
			c.mu.Unlock()
		}
		return nil
	}
	return fmt.Errorf("Error sending HTTP request, errors:", strings.Join(errors, ","))
}

func isNetError(err error) bool {
	switch err.(type) {
	case *net.OpError:
		return true
	}
	return false
}

func (c *Client) Stream(method string, path string, in, out interface{}) (stream stream.Stream, err error) {
	c.mu.RLock()
	pinned := c.pinned
	pinUpdated := c.pinUpdated
	c.mu.RUnlock()
	for i := pinned; i < len(c.servers)+pinned; i++ {
		k := i % len(c.servers)
		hc := c.servers[k]
		stream, err := hc.Stream(method, path, in, out)
		if err != nil {
			continue
		}
		if time.Since(pinUpdated) > PinTTL {
			k = 0 // TTL has timed out, use preffered server on next request
		}
		if k != pinned {
			c.mu.Lock()
			c.pinned = k
			if pinned == 0 {
				// Only restart the TTL if the preferred server was pinned but failed
				c.pinUpdated = time.Now()
			}
			c.mu.Unlock()
		}
		return stream, err
	}
	return nil, err
}

func (c *Client) Get(path string, out interface{}) error {
	return c.Send("GET", path, nil, out)
}

func (c *Client) Put(path string, in, out interface{}) error {
	return c.Send("PUT", path, in, out)
}

func (c *Client) Delete(path string) error {
	return c.Send("DELETE", path, nil, nil)
}

func (c *Client) Ping(url string) error {
	if s := c.serverByHost(url); s != nil {
		return s.Get("/ping", nil)
	}
	return fmt.Errorf("discoverd server not found in server list")
}

func (c *Client) serverByHost(url string) *httpclient.Client {
	for _, s := range c.servers {
		if s.URL == url {
			return s
		}
	}
	return nil
}

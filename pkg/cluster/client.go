// Package cluster implements a client for the Flynn host service.
package cluster

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/attempt"
	"github.com/flynn/flynn/pkg/httpclient"
	"github.com/flynn/flynn/pkg/random"
	"github.com/flynn/flynn/pkg/sse"
	"github.com/flynn/flynn/pkg/stream"
)

// ErrNoServers is returned if no host servers are found
var ErrNoServers = errors.New("cluster: no servers found")

// Attempts is the attempt strategy that is used to connect to the leader.
// It must not be modified after the first call to NewClient.
var Attempts = attempt.Strategy{
	Min:   5,
	Total: 5 * time.Second,
	Delay: 200 * time.Millisecond,
}

// NewClient uses discoverd to dial the local cluster leader and returns
// a client.
func NewClient() (*Client, error) {
	return NewClientWithDial(nil, nil)
}

// A ServiceSetFunc is a function that takes a service name and returns
// a discoverd.ServiceSet.
type ServiceSetFunc func(name string) (discoverd.ServiceSet, error)

// NewClientWithDial uses the provided dial and services to dial the cluster
// leader and returns a client. If dial is nil, the default network dialer is
// used. If services is nil, the default discoverd client is used.
func NewClientWithDial(dial httpclient.DialFunc, services ServiceSetFunc) (*Client, error) {
	client, err := newClient(services)
	if err != nil {
		return nil, err
	}
	if dial == nil {
		dial = net.Dial
	}
	client.dial = dial
	return client, client.start()
}

// ErrNotFound is returned when a resource is not found (HTTP status 404).
var ErrNotFound = errors.New("cluster: resource not found")

func newClient(services ServiceSetFunc) (*Client, error) {
	if services == nil {
		services = discoverd.NewServiceSet
	}
	ss, err := services("flynn-host")
	if err != nil {
		return nil, err
	}
	c := &httpclient.Client{
		ErrPrefix:   "cluster",
		ErrNotFound: ErrNotFound,
		HTTP:        nil,
	}
	return &Client{service: ss, c: c, leaderChange: make(chan struct{})}, nil
}

// NewClient returns a client configured to talk to the leader with the
// identifier id.
func NewClientWithID(id string) (*Client, error) {
	client, err := newClient(nil)
	if err != nil {
		return nil, err
	}
	client.selfID = id
	return client, client.start()
}

// A Client is used to interact with the leader of a Flynn host service cluster
// leader. If the leader changes, the client uses service discovery to connect
// to the new leader automatically.
type Client struct {
	service  discoverd.ServiceSet
	leaderID string
	selfID   string

	dial httpclient.DialFunc
	c    *httpclient.Client
	mtx  sync.RWMutex
	err  error

	leaderChange chan struct{}
}

func (c *Client) start() error {
	firstErr := make(chan error)
	go c.followLeader(firstErr)
	return <-firstErr
}

func (c *Client) followLeader(firstErr chan<- error) {
	for update := range c.service.Leaders() {
		if update == nil {
			if firstErr != nil {
				firstErr <- ErrNoServers
				c.Close()
				return
			}
			continue
		}
		c.mtx.Lock()
		if c.c != nil {
			c.c.Close()
			c.c = nil
		}
		c.leaderID = update.Attrs["id"]
		if c.leaderID != c.selfID {
			c.c.URL = update.Addr // TODO: append port?
		}
		if c.err == nil {
			close(c.leaderChange)
			c.leaderChange = make(chan struct{})
		}
		c.mtx.Unlock()
		if firstErr != nil {
			firstErr <- c.err
			if c.err != nil {
				c.c = nil
				c.Close()
				return
			}
			firstErr = nil
		}
	}
	// TODO: reconnect to discoverd here
}

// NewLeaderSignal returns a channel that strobes exactly once when a new leader
// connection has been established successfully. It is an error to attempt to
// receive more than one value from the channel.
func (c *Client) NewLeaderSignal() <-chan struct{} {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return c.leaderChange
}

// Close disconnects from the server and cleans up internal resources used by
// the client.
func (c *Client) Close() error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.c != nil {
		c.c.Close()
	}
	return c.service.Close()
}

// LeaderID returns the identifier of the current leader.
func (c *Client) LeaderID() string {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return c.leaderID
}

// ListHosts returns a map of host ids to host structures containing metadata
// and job lists.
func (c *Client) ListHosts() ([]host.Host, error) {
	var hosts []host.Host
	return hosts, c.c.Get("/cluster/hosts", &hosts)
}

// AddJobs requests the addition of more jobs to the cluster.
// jobs is a map of host id -> new jobs. Returns the state of the cluster after
// the operation.
func (c *Client) AddJobs(jobs map[string][]*host.Job) (map[string]host.Host, error) {
	var hosts map[string]host.Host
	return hosts, c.c.Post(fmt.Sprintf("/cluster/jobs"), jobs, &hosts)
}

// DialHost dials and returns a host client for the specified host identifier.
func (c *Client) DialHost(id string) (Host, error) {
	// don't lookup addr if leader id == id
	if c.LeaderID() == id {
		return NewHostClient(c.c.URL, nil, c.dial), nil
	}

	services := c.service.Select(map[string]string{"id": id})
	if len(services) == 0 {
		return nil, ErrNoServers
	}
	addr := services[0].Addr
	return NewHostClient(addr, nil, c.dial), nil
}

// RegisterHost is used by the host service to register itself with the leader
// and get a stream of new jobs. It is not used by clients.
func (c *Client) RegisterHost(h *host.Host, jobs chan host.Job) (io.Closer, error) {
	header := http.Header{"Accept": []string{"text/event-stream"}}
	res, err := c.c.RawReq("PUT", fmt.Sprintf("/cluster/hosts/%s", h.ID), header, h, nil)

	if err != nil {
		return nil, err
	}

	stream := JobEventStream{
		Chan: jobs,
		body: res.Body,
	}
	go func() {
		defer func() {
			close(stream.Chan)
			stream.Close()
		}()

		r := bufio.NewReader(stream.body)
		dec := sse.NewDecoder(r)
		for {
			event := host.Job{}
			if err := dec.Decode(&event); err != nil {
				break
			}
			stream.Chan <- event
		}
	}()
	return stream, nil
}

type JobEventStream struct {
	Chan chan<- host.Job
	body io.ReadCloser
}

func (e JobEventStream) Close() error {
	return e.body.Close()
}

// RemoveJobs is used by flynn-host to delete jobs from the cluster state. It
// does not actually kill jobs running on hosts, and must not be used by
// clients.

// TODO: how does this work, since the client method doesn't take a hostID, but
// we clearly need a hostID on the server end
// TODO: do we only support stopping a single job, or do we still take arrays
func (c *Client) RemoveJobs(jobIDs []string) error {
	jobID := jobIDs[0]
	return c.c.Delete(fmt.Sprintf("/cluster/hosts/%s/jobs/%s", c.selfID, jobID))
}

// StreamHostEvents sends a stream of host events from the host to ch.
func (c *Client) StreamHostEvents(ch chan host.HostEvent) (io.Closer, error) {
	header := http.Header{"Accept": []string{"text/event-stream"}}
	res, err := c.c.RawReq("GET", "/cluster/events", header, nil, nil)

	if err != nil {
		return nil, err
	}

	stream := HostEventStream{
		Chan: ch,
		body: res.Body,
	}
	go func() {
		defer func() {
			close(ch)
			stream.Close()
		}()

		r := bufio.NewReader(stream.body)
		dec := sse.NewDecoder(r)
		for {
			event := host.HostEvent{}
			if err := dec.Decode(&event); err != nil {
				break
			}
			stream.Chan <- event
		}
	}()
	return stream, nil
}

type HostEventStream struct {
	Chan chan<- host.HostEvent
	body io.ReadCloser
}

func (e HostEventStream) Close() error {
	return e.body.Close()
}

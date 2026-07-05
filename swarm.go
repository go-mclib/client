package client

import (
	"context"
	"sync"
)

// Swarm manages multiple clients that can be coordinated as a group. It is
// deliberately generic: it knows nothing about what the clients do, so it works
// for command-block cores, PvP bots, or any other multi-client setup.
type Swarm struct {
	mu        sync.RWMutex
	clients   []*Client
	onAllPlay []func()
	played    map[*Client]bool
	allFired  bool
}

// NewSwarm creates a new swarm.
func NewSwarm() *Swarm {
	return &Swarm{played: make(map[*Client]bool)}
}

// NewClient creates a new client within this swarm.
func (s *Swarm) NewClient(address, username string, onlineMode bool) *Client {
	c := New(address, username, onlineMode)
	s.Add(c)
	return c
}

// Add registers an externally-constructed client with this swarm.
func (s *Swarm) Add(c *Client) {
	c.swarm = s
	s.mu.Lock()
	s.clients = append(s.clients, c)
	s.mu.Unlock()
}

// Clients returns a snapshot of all clients in the swarm.
func (s *Swarm) Clients() []*Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Client, len(s.clients))
	copy(out, s.clients)
	return out
}

// Len returns the number of clients in the swarm.
func (s *Swarm) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// Each runs fn against every client concurrently and waits for all to finish.
func (s *Swarm) Each(fn func(*Client)) {
	var wg sync.WaitGroup
	for _, c := range s.Clients() {
		wg.Go(func() { fn(c) })
	}
	wg.Wait()
}

// Assign maps role names to clients in registration order, for separating work
// across the swarm (e.g. "top", "bottom", "audio") rather than duplicating it.
// Extra roles or extra clients beyond the shorter list are ignored.
func (s *Swarm) Assign(roles ...string) map[string]*Client {
	clients := s.Clients()
	out := make(map[string]*Client, min(len(roles), len(clients)))
	for i, role := range roles {
		if i >= len(clients) {
			break
		}
		out[role] = clients[i]
	}
	return out
}

// OnAllPlay registers a callback fired once, when every client has entered the
// play state at least once. Register before Start.
func (s *Swarm) OnAllPlay(cb func()) {
	s.mu.Lock()
	s.onAllPlay = append(s.onAllPlay, cb)
	s.mu.Unlock()
}

// markPlay records that a client reached play and fires OnAllPlay once all have.
func (s *Swarm) markPlay(c *Client) {
	s.mu.Lock()
	s.played[c] = true
	fire := !s.allFired && len(s.played) == len(s.clients)
	if fire {
		s.allFired = true
	}
	cbs := s.onAllPlay
	s.mu.Unlock()

	if fire {
		for _, cb := range cbs {
			cb()
		}
	}
}

// Start connects all clients concurrently.
// Returns the first error from any client, or nil if all exit cleanly.
func (s *Swarm) Start(ctx context.Context) error {
	clients := s.Clients()

	// wire the readiness barrier before any client connects
	for _, c := range clients {
		c.OnPlay(func() { s.markPlay(c) })
	}

	errs := make(chan error, len(clients))
	for _, c := range clients {
		go func() {
			errs <- c.ConnectAndStart(ctx)
		}()
	}

	// return first error
	for range clients {
		if err := <-errs; err != nil {
			return err
		}
	}
	return nil
}

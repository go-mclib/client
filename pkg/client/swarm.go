package client

import (
	"context"
	"sync"
)

// Swarm manages multiple clients that can access each other's modules.
type Swarm struct {
	mu      sync.RWMutex
	clients []*Client
}

// NewSwarm creates a new swarm.
func NewSwarm() *Swarm {
	return &Swarm{}
}

// NewClient creates a new client within this swarm.
func (s *Swarm) NewClient(address, username string, onlineMode bool) *Client {
	c := New(address, username, onlineMode)
	c.swarm = s
	s.mu.Lock()
	s.clients = append(s.clients, c)
	s.mu.Unlock()
	return c
}

// Clients returns all clients in the swarm.
func (s *Swarm) Clients() []*Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Client, len(s.clients))
	copy(out, s.clients)
	return out
}

// Start connects all clients concurrently.
// Returns the first error from any client, or nil if all exit cleanly.
func (s *Swarm) Start(ctx context.Context) error {
	clients := s.Clients()
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

package main

import (
	"encoding/json"
	"os"
	"sync"
)

type greetStore struct {
	Path string
	mu   sync.Mutex
	Seen map[string]bool
}

func newGreetStore(path string) *greetStore {
	return &greetStore{Path: path, Seen: make(map[string]bool)}
}

func (g *greetStore) Load() {
	data, err := os.ReadFile(g.Path)
	if err != nil {
		return
	}
	var m map[string]bool
	if json.Unmarshal(data, &m) == nil {
		g.mu.Lock()
		g.Seen = m
		g.mu.Unlock()
	}
}

func (g *greetStore) Save() {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, _ := json.MarshalIndent(g.Seen, "", "  ")
	_ = os.WriteFile(g.Path, data, 0o644)
}

func (g *greetStore) Mark(name string) {
	g.mu.Lock()
	g.Seen[name] = true
	g.mu.Unlock()
}

func (g *greetStore) Has(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Seen[name]
}

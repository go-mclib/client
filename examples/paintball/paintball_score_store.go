package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

type scoreStore struct {
	Path   string
	mu     sync.Mutex
	Scores map[string]int
}

func newScoreStore(path string) *scoreStore {
	return &scoreStore{Path: path, Scores: make(map[string]int)}
}

func (s *scoreStore) Load() {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return
	}
	var m map[string]int
	if json.Unmarshal(data, &m) == nil {
		s.mu.Lock()
		s.Scores = m
		s.mu.Unlock()
	}
}

func (s *scoreStore) Save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.MarshalIndent(s.Scores, "", "  ")
	_ = os.WriteFile(s.Path, data, 0o644)
}

func (s *scoreStore) AddScore(player string) {
	s.mu.Lock()
	s.Scores[player]++
	s.mu.Unlock()
}

func (s *scoreStore) GetScore(player string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Scores[player]
}

func (s *scoreStore) ProcessChatMessage(text string) bool {
	text = strings.TrimSpace(text)
	if matches := paintRegex.FindStringSubmatch(text); len(matches) == 3 {
		shooter := matches[1]
		s.AddScore(shooter)
		return true
	}
	return false
}

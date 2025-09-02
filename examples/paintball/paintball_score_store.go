package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/go-mclib/client/client"
)

type scoreStore struct {
	Path   string
	mu     sync.Mutex
	Scores map[string]int
}

var paintRegex = regexp.MustCompile(`^(?:You were painted (\w+) by (\w+)|(\w+) painted (\w+))$`)

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

func (s *scoreStore) RemoveScore(player string) {
	s.mu.Lock()
	s.Scores[player]--
	s.mu.Unlock()
}

func (s *scoreStore) GetTopScores() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	scores := make([]string, 0, len(s.Scores))
	for player, score := range s.Scores {
		scores = append(scores, fmt.Sprintf("%s: %d", player, score))
	}
	sort.Slice(scores, func(i, j int) bool {
		return scores[i] > scores[j]
	})

	return scores
}

func (s *scoreStore) ProcessChatMessage(c *client.Client, text string) bool {
	text = strings.TrimSpace(text)
	matches := paintRegex.FindStringSubmatch(text)

	if len(matches) > 0 {
		var shooter, victim string

		if matches[2] != "" {
			// "You were painted COLOR by Username"
			shooter = matches[2]
			victim = c.Username
		} else if matches[3] != "" && matches[4] != "" {
			// "Username1 painted Username2"
			shooter = matches[3]
			victim = matches[4]
		} else {
			return false
		}

		s.AddScore(shooter)
		s.RemoveScore(victim)

		return true
	}
	return false
}

package world

import (
	"cmp"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/chunks"
)

// BlockQuery searches block centers within Radius of (X,Y,Z). Names are exact
// registry names (the minecraft: prefix is optional). Names are ORed; Label is
// an additional case-insensitive substring filter on attached sign lines.
// Radius must be 1..128, Limit 1..50, and Names or Label must be supplied.
type BlockQuery struct {
	X      float64  `json:"x"`
	Y      float64  `json:"y"`
	Z      float64  `json:"z"`
	Radius float64  `json:"radius"`
	Names  []string `json:"names,omitempty"`
	Label  string   `json:"label,omitempty"`
	Limit  int      `json:"limit"`
}

type SignLabel struct {
	X     int      `json:"x"`
	Y     int      `json:"y"`
	Z     int      `json:"z"`
	Front []string `json:"front"`
	Back  []string `json:"back"`
}

type BlockMatch struct {
	X        int         `json:"x"`
	Y        int         `json:"y"`
	Z        int         `json:"z"`
	StateID  int32       `json:"state_id"`
	Name     string      `json:"block"`
	Distance float64     `json:"distance"`
	Labels   []SignLabel `json:"labels"`
}

// Complete describes coverage, not whether Limit omitted known matches.
// Truncated means more observed matches exist than fit in Blocks. An incomplete
// result is nearest among observed matches only, never globally nearest.
type BlockSearchResult struct {
	Blocks       []BlockMatch `json:"blocks"`
	LoadedChunks int          `json:"loaded_chunks"`
	Truncated    bool         `json:"truncated"`
	Complete     bool         `json:"complete"`
}

var (
	errBlockQueryBounds = errors.New("radius must be 1..128 and limit must be 1..50")
	errBlockQueryCenter = errors.New("query center must be finite and inside protocol position bounds")
	errBlockQueryEmpty  = errors.New("names or label is required")
)

// FindBlocks scans only loaded sections. Work is bounded by Radius, storage by
// Limit plus distinct observed states. A single read lock provides a coherent
// snapshot without per-block lock traffic; cancellation is checked each row.
func (m *Module) FindBlocks(ctx context.Context, q BlockQuery) (BlockSearchResult, error) {
	result := BlockSearchResult{Blocks: []BlockMatch{}, Complete: true}
	if err := ctx.Err(); err != nil {
		result.Complete = false
		return result, err
	}
	if err := validateBlockQuery(q); err != nil {
		return BlockSearchResult{}, err
	}
	filter, err := compileBlockFilter(q)
	if err != nil {
		return BlockSearchResult{}, err
	}
	low := [3]int{int(math.Ceil(q.X - q.Radius - .5)), int(math.Ceil(q.Y - q.Radius - .5)), int(math.Ceil(q.Z - q.Radius - .5))}
	high := [3]int{int(math.Floor(q.X + q.Radius - .5)), int(math.Floor(q.Y + q.Radius - .5)), int(math.Floor(q.Z + q.Radius - .5))}
	scan := blockScan{
		q: q, filter: filter, result: &result,
		radius2: q.Radius * q.Radius,
		nearest: make(blockMatchHeap, 0, q.Limit),
	}
	minDY := centerRangeDistance(q.Y, low[1], high[1])
	m.mu.RLock()
	defer m.mu.RUnlock()
	for cx := low[0] >> 4; cx <= high[0]>>4; cx++ {
		for cz := low[2] >> 4; cz <= high[2]>>4; cz++ {
			box := blockBox{
				minX: max(low[0], cx*16), maxX: min(high[0], cx*16+15),
				minZ: max(low[2], cz*16), maxZ: min(high[2], cz*16+15),
			}
			dx := centerRangeDistance(q.X, box.minX, box.maxX)
			dz := centerRangeDistance(q.Z, box.minZ, box.maxZ)
			if dx*dx+minDY*minDY+dz*dz > scan.radius2 {
				continue
			}
			column := m.chunks[chunkKey(int32(cx), int32(cz))]
			if column != nil {
				result.LoadedChunks++
			}
			for sy := low[1] >> 4; sy <= high[1]>>4; sy++ {
				if err := ctx.Err(); err != nil {
					result.Complete = false
					return result, err
				}
				box.minY, box.maxY = max(low[1], sy*16), min(high[1], sy*16+15)
				dy := centerRangeDistance(q.Y, box.minY, box.maxY)
				if dx*dx+dy*dy+dz*dz > scan.radius2 {
					continue
				}
				if column == nil {
					result.Complete = false
					continue
				}
				index := column.SectionIndex(box.minY)
				if index < 0 || column.Sections[index] == nil || column.Sections[index].BlockStates == nil {
					result.Complete = false
					continue
				}
				if err := m.scanSectionLocked(ctx, &scan, column.Sections[index], box); err != nil {
					result.Complete = false
					return result, err
				}
			}
		}
	}
	nearest := scan.nearest
	slices.SortFunc(nearest, compareBlockMatch)
	for i := range nearest {
		nearest[i].Distance = math.Sqrt(nearest[i].Distance)
		if filter.label == "" {
			labels, complete := m.containerLabelsLocked(nearest[i].X, nearest[i].Y, nearest[i].Z)
			nearest[i].Labels = labels
			result.Complete = result.Complete && complete
		}
	}
	result.Blocks = []BlockMatch(nearest)
	result.Truncated = scan.matches > q.Limit
	return result, nil
}

func validateBlockQuery(q BlockQuery) error {
	if math.IsNaN(q.Radius) || q.Radius < 1 || q.Radius > 128 || q.Limit < 1 || q.Limit > 50 {
		return errBlockQueryBounds
	}
	// Positions on the wire use signed 26-bit X/Z and 12-bit Y. Keep float-to-int
	// conversion and chunk arithmetic safe, including queries outside build height.
	if !queryCoordinate(q.X, 33554432) || !queryCoordinate(q.Z, 33554432) || !queryCoordinate(q.Y, 2048) {
		return errBlockQueryCenter
	}
	if len(q.Names) == 0 && q.Label == "" {
		return errBlockQueryEmpty
	}
	return nil
}

// blockFilter decides per block state whether it is a candidate, caching the
// decision for every distinct state observed.
type blockFilter struct {
	names  map[int32]bool // empty: any block
	label  string         // lower-cased; non-empty restricts to containers
	states map[int32]stateFilter
}

type stateFilter struct {
	name    string
	matches bool
}

func compileBlockFilter(q BlockQuery) (*blockFilter, error) {
	f := &blockFilter{
		names:  make(map[int32]bool, len(q.Names)),
		label:  strings.ToLower(q.Label),
		states: make(map[int32]stateFilter),
	}
	for _, name := range q.Names {
		if !strings.Contains(name, ":") {
			name = "minecraft:" + name
		}
		id := blocks.ID(name)
		if id < 0 {
			return nil, fmt.Errorf("unknown block %q", name)
		}
		f.names[id] = true
	}
	return f, nil
}

// state returns the cached decision for state; known is false for a state ID
// outside the block registry, which is never cached or matched.
func (f *blockFilter) state(state int32) (filter stateFilter, known bool) {
	if cached, ok := f.states[state]; ok {
		return cached, true
	}
	id := blocks.StateBlockID(state)
	if id < 0 {
		return stateFilter{}, false
	}
	name := blocks.Name(id)
	filter = stateFilter{name, (len(f.names) == 0 || f.names[id]) && (f.label == "" || isContainer(name))}
	f.states[state] = filter
	return filter, true
}

// blockBox is an inclusive block range inside one chunk section.
type blockBox struct{ minX, maxX, minY, maxY, minZ, maxZ int }

// blockScan accumulates the Limit nearest matches of one FindBlocks query.
type blockScan struct {
	q       BlockQuery
	filter  *blockFilter
	result  *BlockSearchResult
	radius2 float64
	nearest blockMatchHeap // max-heap on distance: nearest[0] is the worst kept
	matches int
}

// offer counts a match and keeps it if it is among the Limit nearest.
func (s *blockScan) offer(candidate BlockMatch) {
	s.matches++
	if len(s.nearest) < s.q.Limit {
		heap.Push(&s.nearest, candidate)
		return
	}
	if compareBlockMatch(candidate, s.nearest[0]) < 0 {
		s.nearest[0] = candidate
		heap.Fix(&s.nearest, 0)
	}
}

func (s *blockScan) match(state int32) (stateFilter, bool) {
	f, known := s.filter.state(state)
	if !known {
		s.result.Complete = false
	}
	return f, f.matches
}

// scanSectionLocked offers every matching block of section inside box and the
// query sphere. Caller holds m.mu.
func (m *Module) scanSectionLocked(ctx context.Context, s *blockScan, section *chunks.Section, box blockBox) error {
	// Empty and other uniform palettes need only one state lookup.
	if section.BlockStates.BitsPerEntry() == 0 {
		if _, ok := s.match(section.BlockStates.Get(0)); !ok {
			return nil
		}
	}
	q := s.q
	for y := box.minY; y <= box.maxY; y++ {
		for z := box.minZ; z <= box.maxZ; z++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for x := box.minX; x <= box.maxX; x++ {
				dx, dy, dz := float64(x)+.5-q.X, float64(y)+.5-q.Y, float64(z)+.5-q.Z
				distance2 := dx*dx + dy*dy + dz*dz
				if distance2 > s.radius2 {
					continue
				}
				state := section.GetBlockState(x&15, y&15, z&15)
				f, ok := s.match(state)
				if !ok {
					continue
				}
				candidate := BlockMatch{X: x, Y: y, Z: z, StateID: state, Name: f.name, Distance: distance2}
				if s.filter.label != "" {
					labels, complete := m.containerLabelsLocked(x, y, z)
					s.result.Complete = s.result.Complete && complete
					if !labelsContain(labels, s.filter.label) {
						continue
					}
					candidate.Labels = labels
				}
				s.offer(candidate)
			}
		}
	}
	return nil
}

func queryCoordinate(value, bound float64) bool {
	return !math.IsNaN(value) && value >= -bound && value < bound
}

func centerRangeDistance(value float64, low, high int) float64 {
	// The closest discrete block center, not merely the section's continuous box.
	center := math.Floor(value)
	center = max(float64(low), min(float64(high), center)) + .5
	return math.Abs(value - center)
}

func labelsContain(labels []SignLabel, query string) bool {
	for _, label := range labels {
		for _, lines := range [][]string{label.Front, label.Back} {
			for _, line := range lines {
				if strings.Contains(strings.ToLower(line), query) {
					return true
				}
			}
		}
	}
	return false
}

func compareBlockMatch(a, b BlockMatch) int {
	return cmp.Or(cmp.Compare(a.Distance, b.Distance), cmp.Compare(a.X, b.X), cmp.Compare(a.Y, b.Y), cmp.Compare(a.Z, b.Z))
}

type blockMatchHeap []BlockMatch

func (h blockMatchHeap) Len() int           { return len(h) }
func (h blockMatchHeap) Less(i, j int) bool { return compareBlockMatch(h[j], h[i]) < 0 }
func (h blockMatchHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *blockMatchHeap) Push(v any)        { *h = append(*h, v.(BlockMatch)) }
func (h *blockMatchHeap) Pop() any          { old := *h; v := old[len(old)-1]; *h = old[:len(old)-1]; return v }

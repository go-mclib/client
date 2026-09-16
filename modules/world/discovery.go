package world

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/go-mclib/data/blocks"
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

// FindBlocks scans only loaded sections. Work is bounded by Radius, storage by
// Limit plus distinct observed states. A single read lock provides a coherent
// snapshot without per-block lock traffic; cancellation is checked each row.
func (m *Module) FindBlocks(ctx context.Context, q BlockQuery) (BlockSearchResult, error) {
	result := BlockSearchResult{Blocks: []BlockMatch{}, Complete: true}
	if err := ctx.Err(); err != nil {
		result.Complete = false
		return result, err
	}
	if math.IsNaN(q.Radius) || q.Radius < 1 || q.Radius > 128 || q.Limit < 1 || q.Limit > 50 {
		return BlockSearchResult{}, fmt.Errorf("radius must be 1..128 and limit must be 1..50")
	}
	// Positions on the wire use signed 26-bit X/Z and 12-bit Y. Keep float-to-int
	// conversion and chunk arithmetic safe, including queries outside build height.
	if !queryCoordinate(q.X, 33554432) || !queryCoordinate(q.Z, 33554432) || !queryCoordinate(q.Y, 2048) {
		return BlockSearchResult{}, fmt.Errorf("query center must be finite and inside protocol position bounds")
	}
	label := strings.ToLower(q.Label)
	if len(q.Names) == 0 && label == "" {
		return BlockSearchResult{}, fmt.Errorf("names or label is required")
	}
	names := make(map[int32]bool, len(q.Names))
	for _, name := range q.Names {
		if !strings.Contains(name, ":") {
			name = "minecraft:" + name
		}
		id := blocks.BlockID(name)
		if id < 0 {
			return BlockSearchResult{}, fmt.Errorf("unknown block %q", name)
		}
		names[id] = true
	}
	type stateFilter struct {
		name    string
		matches bool
	}
	states := make(map[int32]stateFilter)
	filter := func(state int32) stateFilter {
		if f, ok := states[state]; ok {
			return f
		}
		id := blocks.StateBlockID(state)
		if id < 0 {
			result.Complete = false
			return stateFilter{}
		}
		name := blocks.BlockName(id)
		f := stateFilter{name, id >= 0 && (len(names) == 0 || names[id]) && (label == "" || isContainer(name))}
		states[state] = f
		return f
	}
	low := [3]int{int(math.Ceil(q.X - q.Radius - .5)), int(math.Ceil(q.Y - q.Radius - .5)), int(math.Ceil(q.Z - q.Radius - .5))}
	high := [3]int{int(math.Floor(q.X + q.Radius - .5)), int(math.Floor(q.Y + q.Radius - .5)), int(math.Floor(q.Z + q.Radius - .5))}
	radius2 := q.Radius * q.Radius
	minDY := centerRangeDistance(q.Y, low[1], high[1])
	nearest := make(blockMatchHeap, 0, q.Limit)
	matches := 0
	m.mu.RLock()
	defer m.mu.RUnlock()
	for cx := low[0] >> 4; cx <= high[0]>>4; cx++ {
		for cz := low[2] >> 4; cz <= high[2]>>4; cz++ {
			minX, maxX := max(low[0], cx*16), min(high[0], cx*16+15)
			minZ, maxZ := max(low[2], cz*16), min(high[2], cz*16+15)
			dx := centerRangeDistance(q.X, minX, maxX)
			dz := centerRangeDistance(q.Z, minZ, maxZ)
			if dx*dx+minDY*minDY+dz*dz > radius2 {
				continue
			}
			column := m.chunks[ChunkKey(int32(cx), int32(cz))]
			if column != nil {
				result.LoadedChunks++
			}
			for sy := low[1] >> 4; sy <= high[1]>>4; sy++ {
				if err := ctx.Err(); err != nil {
					result.Complete = false
					return result, err
				}
				minY, maxY := max(low[1], sy*16), min(high[1], sy*16+15)
				dy := centerRangeDistance(q.Y, minY, maxY)
				if dx*dx+dy*dy+dz*dz > radius2 {
					continue
				}
				if column == nil {
					result.Complete = false
					continue
				}
				index := column.SectionIndex(minY)
				if index < 0 || column.Sections[index] == nil || column.Sections[index].BlockStates == nil {
					result.Complete = false
					continue
				}
				section := column.Sections[index]
				// Empty and other uniform palettes need only one state lookup.
				if section.BlockStates.BitsPerEntry() == 0 && !filter(section.BlockStates.Get(0)).matches {
					continue
				}
				for y := minY; y <= maxY; y++ {
					for z := minZ; z <= maxZ; z++ {
						if err := ctx.Err(); err != nil {
							result.Complete = false
							return result, err
						}
						for x := minX; x <= maxX; x++ {
							dx, dy, dz := float64(x)+.5-q.X, float64(y)+.5-q.Y, float64(z)+.5-q.Z
							distance2 := dx*dx + dy*dy + dz*dz
							if distance2 > radius2 {
								continue
							}
							state := section.GetBlockState(x&15, y&15, z&15)
							f := filter(state)
							if !f.matches {
								continue
							}
							candidate := BlockMatch{X: x, Y: y, Z: z, StateID: state, Name: f.name, Distance: distance2}
							if label != "" {
								labels, complete := m.containerLabelsLocked(x, y, z)
								result.Complete = result.Complete && complete
								if !labelsContain(labels, label) {
									continue
								}
								candidate.Labels = labels
							}
							matches++
							if len(nearest) == q.Limit && !blockMatchLess(candidate, nearest[0]) {
								continue
							}
							if len(nearest) == q.Limit {
								nearest[0] = candidate
								heap.Fix(&nearest, 0)
							} else {
								heap.Push(&nearest, candidate)
							}
						}
					}
				}
			}
		}
	}
	sort.Slice(nearest, func(i, j int) bool { return blockMatchLess(nearest[i], nearest[j]) })
	for i := range nearest {
		nearest[i].Distance = math.Sqrt(nearest[i].Distance)
		if label == "" {
			labels, complete := m.containerLabelsLocked(nearest[i].X, nearest[i].Y, nearest[i].Z)
			nearest[i].Labels = labels
			result.Complete = result.Complete && complete
		}
	}
	result.Blocks = []BlockMatch(nearest)
	result.Truncated = matches > q.Limit
	return result, nil
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

func blockMatchLess(a, b BlockMatch) bool {
	if a.Distance != b.Distance {
		return a.Distance < b.Distance
	}
	if a.X != b.X {
		return a.X < b.X
	}
	if a.Y != b.Y {
		return a.Y < b.Y
	}
	return a.Z < b.Z
}

type blockMatchHeap []BlockMatch

func (h blockMatchHeap) Len() int           { return len(h) }
func (h blockMatchHeap) Less(i, j int) bool { return blockMatchLess(h[j], h[i]) }
func (h blockMatchHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *blockMatchHeap) Push(v any)        { *h = append(*h, v.(BlockMatch)) }
func (h *blockMatchHeap) Pop() any          { old := *h; v := old[len(old)-1]; *h = old[:len(old)-1]; return v }

package pathfinding

import (
	"math"
	"sync"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/collisions"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/physics"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/client/pkg/client/modules/world"
	jp "github.com/go-mclib/protocol/java_protocol"
)

const ModuleName = "pathfinding"

type Module struct {
	client *client.Client

	MaxNodes int // maximum A* nodes to explore (default: 10000)

	mu         sync.Mutex
	navigating bool
	path       []PathNode
	pathIndex  int
	stuckTicks int
	lastNavX   float64
	lastNavZ   float64

	onPathFound          []func(path []PathNode)
	onNavigationComplete []func(reached bool)
}

func New() *Module {
	return &Module{
		MaxNodes: DefaultMaxNodes,
	}
}

func (m *Module) Name() string                  { return ModuleName }
func (m *Module) HandlePacket(_ *jp.WirePacket) {}

func (m *Module) Init(c *client.Client) {
	m.client = c

	// register tick callback for navigation
	p := physics.From(c)
	if p != nil {
		p.OnTick(func() {
			m.navigationTick()
		})
	}
}

func (m *Module) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.navigating = false
	m.path = nil
	m.pathIndex = 0
	m.stuckTicks = 0
}

func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// events

func (m *Module) OnPathFound(cb func(path []PathNode)) {
	m.onPathFound = append(m.onPathFound, cb)
}

func (m *Module) OnNavigationComplete(cb func(reached bool)) {
	m.onNavigationComplete = append(m.onNavigationComplete, cb)
}

// FindPath computes a path from the player's current position to the goal.
func (m *Module) FindPath(goalX, goalY, goalZ int) ([]PathNode, error) {
	s := self.From(m.client)
	w := world.From(m.client)
	col := collisions.From(m.client)
	ents := entities.From(m.client)
	if s == nil || w == nil || col == nil {
		return nil, nil
	}

	startX := int(math.Floor(float64(s.X)))
	startY := int(math.Floor(float64(s.Y)))
	startZ := int(math.Floor(float64(s.Z)))

	maxNodes := m.MaxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}

	path, err := findPath(w, col, ents, startX, startY, startZ, goalX, goalY, goalZ, maxNodes)
	if err != nil {
		return nil, err
	}

	for _, cb := range m.onPathFound {
		cb(path)
	}

	return path, nil
}

// NavigateTo computes a path and begins navigating to the goal.
// Navigation is driven by physics tick callbacks.
func (m *Module) NavigateTo(goalX, goalY, goalZ int) error {
	path, err := m.FindPath(goalX, goalY, goalZ)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.path = path
	m.pathIndex = 0
	m.navigating = true
	m.stuckTicks = 0
	m.mu.Unlock()

	return nil
}

// Stop cancels the current navigation.
func (m *Module) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.navigating {
		m.navigating = false
		m.path = nil

		// clear physics input
		p := physics.From(m.client)
		if p != nil {
			p.SetInput(0, 0, false, false)
			p.SetSprinting(false)
		}
	}
}

// IsNavigating returns true if the bot is currently navigating.
func (m *Module) IsNavigating() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.navigating
}

func (m *Module) navigationTick() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.navigating || len(m.path) == 0 {
		return
	}

	s := self.From(m.client)
	p := physics.From(m.client)
	if s == nil || p == nil {
		return
	}

	x := float64(s.X)
	y := float64(s.Y)
	z := float64(s.Z)

	// get current waypoint
	if m.pathIndex >= len(m.path) {
		m.completeNavigation(true)
		return
	}

	wp := m.path[m.pathIndex]
	wpX := float64(wp.X) + 0.5
	wpY := float64(wp.Y)
	wpZ := float64(wp.Z) + 0.5

	dx := wpX - x
	dy := wpY - y
	dz := wpZ - z
	horizDist := math.Sqrt(dx*dx + dz*dz)

	// reached waypoint?
	if horizDist < 0.5 && math.Abs(dy) < 1.0 {
		m.pathIndex++
		if m.pathIndex >= len(m.path) {
			m.completeNavigation(true)
			return
		}
		m.stuckTicks = 0
		// update waypoint
		wp = m.path[m.pathIndex]
		wpX = float64(wp.X) + 0.5
		wpY = float64(wp.Y)
		wpZ = float64(wp.Z) + 0.5
		dx = wpX - x
		dy = wpY - y
		dz = wpZ - z
		horizDist = math.Sqrt(dx*dx + dz*dz)
	}

	// face waypoint
	_ = s.LookAt(wpX, wpY+playerHeight*0.5, wpZ)

	// set movement input
	jumping := dy > 0.5 // jump if waypoint is above
	sprinting := horizDist > 5.0 && !jumping

	p.SetInput(1.0, 0, jumping, false)
	p.SetSprinting(sprinting)

	// stuck detection
	moveDist := math.Sqrt((x-m.lastNavX)*(x-m.lastNavX) + (z-m.lastNavZ)*(z-m.lastNavZ))
	if moveDist < 0.01 {
		m.stuckTicks++
	} else {
		m.stuckTicks = 0
	}
	m.lastNavX = x
	m.lastNavZ = z

	// repath after being stuck for 40 ticks (2 seconds)
	if m.stuckTicks > 40 {
		m.navigating = false
		p.SetInput(0, 0, false, false)
		p.SetSprinting(false)

		for _, cb := range m.onNavigationComplete {
			cb(false)
		}
	}
}

func (m *Module) completeNavigation(reached bool) {
	m.navigating = false
	m.path = nil

	p := physics.From(m.client)
	if p != nil {
		p.SetInput(0, 0, false, false)
		p.SetSprinting(false)
	}

	for _, cb := range m.onNavigationComplete {
		cb(reached)
	}
}

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

	mu            sync.Mutex
	navigating    bool
	path          []PathNode
	pathIndex     int
	stuckTicks    int
	retreatTicks  int // countdown for corner retreat phase
	retreatCycles int // number of retreat cycles since last progress
	lastNavX      float64
	lastNavZ      float64
	goalX         float64
	goalY         float64
	goalZ         float64

	// saved sprint/sneak state to restore after navigation
	savedSprinting bool
	savedSneaking  bool

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
	m.retreatTicks = 0
	m.retreatCycles = 0
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
func (m *Module) FindPath(goalX, goalY, goalZ float64) ([]PathNode, error) {
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

	gx := int(math.Floor(goalX))
	gy := int(math.Floor(goalY))
	gz := int(math.Floor(goalZ))

	maxNodes := m.MaxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}

	path, err := findPath(w, col, ents, startX, startY, startZ, gx, gy, gz, maxNodes)
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
func (m *Module) NavigateTo(goalX, goalY, goalZ float64) error {
	path, err := m.FindPath(goalX, goalY, goalZ)
	if err != nil {
		return err
	}

	s := self.From(m.client)

	m.mu.Lock()
	m.path = path
	m.pathIndex = 0
	m.navigating = true
	m.stuckTicks = 0
	m.retreatTicks = 0
	m.retreatCycles = 0
	m.goalX = goalX
	m.goalY = goalY
	m.goalZ = goalZ
	if s != nil {
		m.savedSprinting = s.Sprinting
		m.savedSneaking = s.Sneaking
	}
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

		// clear physics input, restore sprint/sneak state
		p := physics.From(m.client)
		if p != nil {
			p.SetInput(0, 0, false)
		}
		s := self.From(m.client)
		if s != nil {
			s.Sprinting = m.savedSprinting
			s.Sneaking = m.savedSneaking
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
	w := world.From(m.client)
	col := collisions.From(m.client)
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

	// proactive obstruction check: verify upcoming waypoints are still passable
	if w != nil && col != nil {
		for i := m.pathIndex; i < len(m.path) && i < m.pathIndex+3; i++ {
			node := m.path[i]
			if i == len(m.path)-1 {
				break // don't check the goal node
			}
			cost, _ := moveCost(w, col, nil, node.X, node.Y, node.Z)
			if cost < 0 {
				// path is obstructed, attempt re-path
				if m.tryRepath() {
					return
				}
				m.completeNavigation(false)
				return
			}
		}
	}

	wp := m.path[m.pathIndex]
	isLastWaypoint := m.pathIndex == len(m.path)-1

	// use exact float goal for the final waypoint
	var wpX, wpY, wpZ float64
	if isLastWaypoint {
		wpX, wpY, wpZ = m.goalX, m.goalY, m.goalZ
	} else {
		wpX = float64(wp.X) + 0.5
		wpY = float64(wp.Y)
		wpZ = float64(wp.Z) + 0.5
	}

	dx := wpX - x
	dy := wpY - y
	dz := wpZ - z
	horizDist := math.Sqrt(dx*dx + dz*dz)

	// reached waypoint?
	threshold := 0.5
	vertThreshold := 1.0
	if isLastWaypoint {
		threshold = 0.3
	}
	if wp.Jump {
		threshold = 0.8 // more forgiving for high-velocity jump landings
		vertThreshold = 1.5
	}
	if horizDist < threshold && math.Abs(dy) < vertThreshold {
		m.pathIndex++
		if m.pathIndex >= len(m.path) {
			m.completeNavigation(true)
			return
		}
		m.stuckTicks = 0
		m.retreatTicks = 0
		m.retreatCycles = 0
		// update waypoint
		wp = m.path[m.pathIndex]
		isLastWaypoint = m.pathIndex == len(m.path)-1
		if isLastWaypoint {
			wpX, wpY, wpZ = m.goalX, m.goalY, m.goalZ
		} else {
			wpX = float64(wp.X) + 0.5
			wpY = float64(wp.Y)
			wpZ = float64(wp.Z) + 0.5
		}
		dx = wpX - x
		dy = wpY - y
		dz = wpZ - z
		horizDist = math.Sqrt(dx*dx + dz*dz)
	}

	// wall-slide: when hitting a wall, adjust facing to slide along
	// the unblocked axis. When stuck for several ticks with wall contact,
	// enter retreat mode to escape corners. After multiple retreat cycles,
	// trigger repath.
	lookX, lookZ := wpX, wpZ
	if m.retreatTicks > 0 {
		// persisted retreat: face away from waypoint
		lookX = x - dx
		lookZ = z - dz
		m.retreatTicks--
	} else if p.HorizontalCollision && m.stuckTicks > 3 {
		// stuck for several ticks with wall contact — escalate to retreat
		// (catches both simultaneous corner AND alternating single-axis blocks)
		m.retreatTicks = 8
		m.retreatCycles++
		lookX = x - dx
		lookZ = z - dz
	} else if p.HorizontalCollision {
		if p.XCollision && p.ZCollision {
			// immediate corner: both axes blocked
			m.retreatTicks = 8
			m.retreatCycles++
			lookX = x - dx
			lookZ = z - dz
		} else if p.XCollision {
			lookX = x
			lookZ = z + dz
		} else {
			lookX = x + dx
			lookZ = z
		}
	}
	s.LookAt(lookX, wpY+playerHeight, lookZ)

	// set movement input
	sneaking := s.Sneaking || wp.Sneaking
	var jumping, sprinting bool
	if wp.Jump {
		// sprint-jump: always sprint (required for distance)
		sprinting = true
		jumping = p.OnGround
		sneaking = false
	} else {
		jumping = dy > 0.5 && p.OnGround
		sprinting = s.Sprinting && horizDist > 5.0 && !sneaking
	}

	s.Sneaking = sneaking
	s.Sprinting = sprinting
	p.SetInput(1.0, 0, jumping)

	// stuck detection
	if m.retreatTicks <= 0 {
		moveDist := math.Sqrt((x-m.lastNavX)*(x-m.lastNavX) + (z-m.lastNavZ)*(z-m.lastNavZ))
		if moveDist < 0.01 {
			m.stuckTicks++
		} else {
			m.stuckTicks = 0
		}
	}
	m.lastNavX = x
	m.lastNavZ = z

	// repath after being stuck for 40 ticks or 3 retreat cycles
	if m.stuckTicks > 40 || m.retreatCycles > 3 {
		if m.tryRepath() {
			return
		}
		m.completeNavigation(false)
	}
}

// tryRepath attempts to recompute a path to the current goal.
// Must be called with m.mu held. Returns true if a new path was found.
func (m *Module) tryRepath() bool {
	s := self.From(m.client)
	w := world.From(m.client)
	col := collisions.From(m.client)
	ents := entities.From(m.client)
	if s == nil || w == nil || col == nil {
		return false
	}

	startX := int(math.Floor(float64(s.X)))
	startY := int(math.Floor(float64(s.Y)))
	startZ := int(math.Floor(float64(s.Z)))

	gx := int(math.Floor(m.goalX))
	gy := int(math.Floor(m.goalY))
	gz := int(math.Floor(m.goalZ))

	maxNodes := m.MaxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}

	path, err := findPath(w, col, ents, startX, startY, startZ, gx, gy, gz, maxNodes)
	if err != nil {
		return false
	}

	m.path = path
	m.pathIndex = 0
	m.stuckTicks = 0
	m.retreatTicks = 0
	m.retreatCycles = 0
	return true
}

func (m *Module) completeNavigation(reached bool) {
	m.navigating = false
	m.path = nil

	p := physics.From(m.client)
	if p != nil {
		p.SetInput(0, 0, false)
	}
	s := self.From(m.client)
	if s != nil {
		s.Sprinting = m.savedSprinting
		s.Sneaking = m.savedSneaking
	}

	for _, cb := range m.onNavigationComplete {
		cb(reached)
	}
}

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
	retreatTicks  int
	retreatCycles int
	lastNavX      float64
	lastNavZ      float64
	goalX         float64
	goalY         float64
	goalZ         float64

	// door interaction state
	doorWaitTicks int  // countdown while waiting for door to open
	doorOpened    bool // whether we already sent the interact packet

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
	m.doorWaitTicks = 0
	m.doorOpened = false
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
	p := physics.From(m.client)
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

	// get current physics params for jump simulation
	var jumpPower, effectiveSpeed float64
	if p != nil {
		jumpPower = p.GetJumpPower()
		effectiveSpeed = p.GetEffectiveSpeed()
	}

	path, err := findPath(w, col, ents, startX, startY, startZ, gx, gy, gz, maxNodes, jumpPower, effectiveSpeed)
	if err != nil {
		return nil, err
	}

	for _, cb := range m.onPathFound {
		cb(path)
	}

	return path, nil
}

// NavigateTo computes a path and begins navigating to the goal.
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
	m.doorWaitTicks = 0
	m.doorOpened = false
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

	if m.pathIndex >= len(m.path) {
		m.completeNavigation(true)
		return
	}

	// proactive obstruction check: verify upcoming waypoints are still passable
	if w != nil && col != nil {
		for i := m.pathIndex; i < len(m.path) && i < m.pathIndex+3; i++ {
			node := m.path[i]
			if i == len(m.path)-1 {
				break // don't check goal
			}
			cost, _ := moveCost(w, col, nil, node.X, node.Y, node.Z)
			if cost < 0 {
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

	// door interaction: wait for door to open before proceeding
	if wp.InteractDoor && m.doorWaitTicks > 0 {
		m.doorWaitTicks--
		p.SetInput(0, 0, false) // stop while waiting
		return
	}

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
	threshold := 0.35
	vertThreshold := 1.0
	if isLastWaypoint {
		threshold = 0.3
	}
	if wp.Jump {
		threshold = 0.8
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
		m.doorWaitTicks = 0
		m.doorOpened = false

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

	// door interaction: interact with door when close enough
	if wp.InteractDoor && !m.doorOpened {
		doorDist := math.Sqrt(
			math.Pow(float64(wp.DoorX)+0.5-x, 2) +
				math.Pow(float64(wp.DoorZ)+0.5-z, 2),
		)
		if doorDist < 2.5 {
			// look at the door block
			s.LookAt(float64(wp.DoorX)+0.5, float64(wp.DoorY)+0.5, float64(wp.DoorZ)+0.5)
			// right-click the door
			_ = m.client.InteractBlock(wp.DoorX, wp.DoorY, wp.DoorZ, 0, 0, 0.5, 0.5, 0.5)
			m.doorOpened = true
			m.doorWaitTicks = 4 // wait a few ticks for the server to process
			p.SetInput(0, 0, false)
			return
		}
	}

	// wall-slide and retreat logic
	lookX, lookZ := wpX, wpZ
	if m.retreatTicks > 0 {
		lookX = x - dx
		lookZ = z - dz
		m.retreatTicks--
	} else if p.HorizontalCollision && m.stuckTicks > 3 {
		m.retreatTicks = 8
		m.retreatCycles++
		lookX = x - dx
		lookZ = z - dz
	} else if p.HorizontalCollision {
		if p.XCollision && p.ZCollision {
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

	// movement input
	sneaking := s.Sneaking || wp.Sneaking
	var jumping, sprinting bool
	if wp.Jump {
		sprinting = true
		sneaking = false

		// edge-jumping: wait until near the edge of the block before jumping
		distFromEdge := distToBlockEdge(x, z, dx, dz)
		jumping = p.OnGround && distFromEdge < 0.3
	} else {
		// no jumping for regular movement — step-ups are handled by physics
		jumping = false

		// sprint when moving straight and far enough ahead
		if !sneaking && horizDist > 2.0 {
			sprinting = shouldSprint(m.path, m.pathIndex, x, z)
		}
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

	if m.stuckTicks > 40 || m.retreatCycles > 3 {
		if m.tryRepath() {
			return
		}
		m.completeNavigation(false)
	}
}

// tryRepath attempts to recompute a path to the current goal.
func (m *Module) tryRepath() bool {
	s := self.From(m.client)
	w := world.From(m.client)
	col := collisions.From(m.client)
	ents := entities.From(m.client)
	p := physics.From(m.client)
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

	var jumpPower, effectiveSpeed float64
	if p != nil {
		jumpPower = p.GetJumpPower()
		effectiveSpeed = p.GetEffectiveSpeed()
	}

	path, err := findPath(w, col, ents, startX, startY, startZ, gx, gy, gz, maxNodes, jumpPower, effectiveSpeed)
	if err != nil {
		return false
	}

	m.path = path
	m.pathIndex = 0
	m.stuckTicks = 0
	m.retreatTicks = 0
	m.retreatCycles = 0
	m.doorWaitTicks = 0
	m.doorOpened = false
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

// distToBlockEdge returns the distance from (x,z) to the block edge in the
// direction of (dx,dz). Used for timing parkour edge-jumps.
func distToBlockEdge(x, z, dx, dz float64) float64 {
	// determine primary movement axis
	if math.Abs(dx) > math.Abs(dz) {
		// X axis dominant
		bx := math.Floor(x)
		if dx > 0 {
			return (bx + 1) - x
		}
		return x - bx
	}
	// Z axis dominant
	bz := math.Floor(z)
	if dz > 0 {
		return (bz + 1) - z
	}
	return z - bz
}

// shouldSprint returns true if the bot should sprint for the current segment.
// Sprints when the next few waypoints are roughly in a straight line.
func shouldSprint(path []PathNode, currentIdx int, x, z float64) bool {
	if currentIdx >= len(path)-1 {
		return false
	}

	current := path[currentIdx]
	next := current
	if currentIdx+1 < len(path) {
		next = path[currentIdx+1]
	}

	// check if direction changes significantly in the next 2 waypoints
	dx1 := float64(current.X) + 0.5 - x
	dz1 := float64(current.Z) + 0.5 - z
	dx2 := float64(next.X) - float64(current.X)
	dz2 := float64(next.Z) - float64(current.Z)

	// dot product of direction vectors (normalized)
	len1 := math.Sqrt(dx1*dx1 + dz1*dz1)
	len2 := math.Sqrt(dx2*dx2 + dz2*dz2)
	if len1 < 0.1 || len2 < 0.1 {
		return true // too close to tell direction, sprint
	}

	dot := (dx1*dx2 + dz1*dz2) / (len1 * len2)
	// sprint if the turn angle is less than ~45 degrees (cos 45 ≈ 0.707)
	return dot > 0.6
}

package pathfinding

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const ModuleName = "pathfinding"

type Module struct {
	client *client.Client

	// deps (self/world/collisions/physics required, entities optional)
	self       *self.Module
	world      *world.Module
	collisions *collisions.Module
	physics    *physics.Module
	entities   *entities.Module

	MaxNodes int // maximum A* nodes to explore (default: 10000)

	mu                   sync.Mutex
	navigating           bool
	generation           uint64
	navigationContext    context.Context
	cancel               context.CancelFunc
	path                 []PathNode
	pathIndex            int
	stuckTicks           int
	retreatTicks         int
	retreatCycles        int
	bestWaypointDistance float64
	goalX                float64
	goalY                float64
	goalZ                float64

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
	c.OnTransfer(m.Reset)
}

func (m *Module) Wire() error {
	m.self = self.From(m.client)
	m.world = world.From(m.client)
	m.collisions = collisions.From(m.client)
	m.physics = physics.From(m.client)
	if m.self == nil || m.world == nil || m.collisions == nil || m.physics == nil {
		return fmt.Errorf("requires self, world, collisions and physics modules")
	}
	m.entities = entities.From(m.client) // optional: avoids pathing through entities

	m.physics.OnTick(func() {
		m.navigationTick()
	})
	return nil
}

func (m *Module) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	if m.cancel != nil {
		m.cancel()
	}
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

func (m *Module) OnPathFound(cb func(path []PathNode)) {
	m.onPathFound = append(m.onPathFound, cb)
}

func (m *Module) OnNavigationComplete(cb func(reached bool)) {
	m.onNavigationComplete = append(m.onNavigationComplete, cb)
}

// FindPath computes a path from the player's current position to the goal.
func (m *Module) FindPath(goalX, goalY, goalZ float64) ([]PathNode, error) {
	return m.FindPathContext(context.Background(), goalX, goalY, goalZ)
}

// FindPathContext bounds CPU work with the caller's cancellation as well as MaxNodes.
func (m *Module) FindPathContext(ctx context.Context, goalX, goalY, goalZ float64) ([]PathNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.self
	w := m.world
	col := m.collisions
	ents := m.entities
	cfg := m.simulationConfig()

	sx, sy, sz := s.Position()
	startX := int(math.Floor(sx))
	startY := int(math.Floor(sy))
	startZ := int(math.Floor(sz))

	gx := int(math.Floor(goalX))
	gy := int(math.Floor(goalY))
	gz := int(math.Floor(goalZ))
	if math.IsNaN(goalX) || math.IsNaN(goalY) || math.IsNaN(goalZ) ||
		math.IsInf(goalX, 0) || math.IsInf(goalY, 0) || math.IsInf(goalZ, 0) {
		return nil, fmt.Errorf("pathfinding: non-finite goal")
	}
	if !nodeLoaded(w, startX, startY, startZ, cfg) || !nodeLoaded(w, gx, gy, gz, cfg) {
		return nil, fmt.Errorf("pathfinding: start or goal terrain is unloaded")
	}
	fy, ok := standSurface(col, gx, gy, gz, cfg.sneakingHeight, cfg)
	if !ok || !terrainLoaded(w, goalX, fy, goalZ, cfg.height, cfg) ||
		!col.CanFitAt(goalX, fy, goalZ, cfg.width, cfg.sneakingHeight) ||
		!col.IsOnGround(goalX, fy, goalZ, cfg.width) {
		return nil, fmt.Errorf("pathfinding: goal has no support or headroom")
	}

	maxNodes := m.MaxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}

	path, err := findPath(ctx, w, col, ents, startX, startY, startZ, gx, gy, gz, maxNodes, cfg)
	if err != nil {
		return nil, err
	}
	for _, node := range path {
		if !nodeLoaded(w, node.X, node.Y, node.Z, cfg) {
			return nil, fmt.Errorf("pathfinding: route crosses unloaded terrain")
		}
	}

	for _, cb := range m.onPathFound {
		cb(path)
	}

	return path, nil
}

// NavigateTo computes a path and begins navigating to the goal.
func (m *Module) NavigateTo(goalX, goalY, goalZ float64) error {
	return m.NavigateToContext(context.Background(), goalX, goalY, goalZ)
}

// NavigateToContext keeps planning, input and queued door interactions scoped
// to one cancellable navigation operation.
func (m *Module) NavigateToContext(parent context.Context, goalX, goalY, goalZ float64) error {
	m.Stop()
	m.mu.Lock()
	m.generation++
	ctx, cancel := context.WithCancel(parent)
	m.navigationContext, m.cancel = ctx, cancel
	generation := m.generation
	m.mu.Unlock()
	path, err := m.FindPathContext(ctx, goalX, goalY, goalZ)
	if err != nil {
		cancel()
		return err
	}

	m.mu.Lock()
	if generation != m.generation || ctx.Err() != nil {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("pathfinding: navigation cancelled")
	}
	m.path = path
	m.pathIndex = 0
	m.navigating = true
	m.stuckTicks = 0
	m.retreatTicks = 0
	m.retreatCycles = 0
	m.bestWaypointDistance = math.Inf(1)
	m.doorWaitTicks = 0
	m.doorOpened = false
	m.goalX = goalX
	m.goalY = goalY
	m.goalZ = goalZ
	m.savedSprinting = m.self.Sprinting()
	m.savedSneaking = m.self.Sneaking()
	m.mu.Unlock()
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.navigating && m.generation == generation {
			m.completeNavigation(false)
		}
	}()

	return nil
}

// Stop cancels the current navigation.
func (m *Module) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	if m.cancel != nil {
		m.cancel()
	}

	if m.navigating {
		m.navigating = false
		m.path = nil

		m.physics.SetInput(0, 0, false)
		m.self.SetSprinting(m.savedSprinting)
		m.self.SetSneaking(m.savedSneaking)
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
	if m.navigating && m.navigationContext != nil && m.navigationContext.Err() != nil {
		m.completeNavigation(false)
		return
	}

	if !m.navigating || len(m.path) == 0 {
		return
	}

	s := m.self
	p := m.physics
	cfg := m.simulationConfig()

	x, y, z := s.Position()

	if m.pathIndex >= len(m.path) {
		m.completeNavigation(true)
		return
	}

	if m.handleObstructions(cfg) {
		return
	}

	wp := m.path[m.pathIndex]
	isLastWaypoint := m.pathIndex == len(m.path)-1

	// door interaction: wait for door to open before proceeding
	if wp.InteractDoor && m.doorWaitTicks > 0 {
		m.doorWaitTicks--
		p.SetInput(0, 0, false) // stop while waiting
		return
	}

	wpX, wpY, wpZ := m.waypointTarget(wp, isLastWaypoint, cfg)
	dx := wpX - x
	dy := wpY - y
	dz := wpZ - z
	horizDist := math.Sqrt(dx*dx + dz*dz)

	if waypointReached(wp, isLastWaypoint, horizDist, dy) {
		if !p.IsOnGround() {
			// Crossing a landing column in midair is not a completed jump.
			p.SetInput(0, 0, false)
			return
		}
		if !m.advanceWaypoint() {
			return
		}
		wp = m.path[m.pathIndex]
		isLastWaypoint = m.pathIndex == len(m.path)-1
		wpX, wpY, wpZ = m.waypointTarget(wp, isLastWaypoint, cfg)
		dx = wpX - x
		dy = wpY - y
		dz = wpZ - z
		horizDist = math.Sqrt(dx*dx + dz*dz)
	}

	// door interaction: interact with door when close enough
	if m.handleDoor(wp, x, z) {
		return
	}

	// Walking edges already passed automatic-step sweeps; stairs need no jump.
	lookX, lookZ := m.lookTarget(wp.Jump, x, z, dx, dz, wpX, wpZ)
	s.LookAt(lookX, wpY+cfg.height, lookZ)

	sneaking, sprinting, jumping := m.selectMovementInput(wp, x, z, horizDist)
	s.SetSneaking(sneaking)
	s.SetSprinting(sprinting)
	p.SetInput(1.0, 0, jumping)

	m.detectStuck(horizDist)
}

// handleObstructions proactively verifies upcoming waypoints are still passable,
// repathing or aborting when blocked; returns true when navigation was handled.
func (m *Module) handleObstructions(cfg simulationConfig) bool {
	for i := m.pathIndex; i < len(m.path) && i < m.pathIndex+3; i++ {
		node := m.path[i]
		if !nodeLoaded(m.world, node.X, node.Y, node.Z, cfg) {
			m.completeNavigation(false)
			return true
		}
		if node.InteractDoor && (i != m.pathIndex || !m.doorOpened || m.doorWaitTicks > 0) {
			continue
		}
		cost, _, destY := moveCost(m.world, m.collisions, nil, node.X, node.Y, node.Z, cfg)
		blocked := cost < 0
		if !blocked && i > m.pathIndex && !node.Jump && !node.InteractDoor {
			prev := m.path[i-1]
			if !prev.InteractDoor {
				height := cfg.height
				if node.Sneaking {
					height = cfg.sneakingHeight
				}
				srcY := nodeSurfaceY(m.collisions, prev.X, prev.Y, prev.Z, cfg)
				blocked = !canPassBetween(m.world, m.collisions, prev.X, prev.Z, node.X, node.Z, srcY, math.Max(srcY, destY), height, cfg)
			}
		}
		if blocked {
			if m.tryRepath() {
				return true
			}
			m.completeNavigation(false)
			return true
		}
	}
	return false
}

// waypointTarget returns the position to aim for at a waypoint, using the exact
// float goal for the final waypoint.
func (m *Module) waypointTarget(wp PathNode, isLast bool, cfg simulationConfig) (wpX, wpY, wpZ float64) {
	if isLast {
		return m.goalX, waypointTargetY(m.collisions, wp, cfg), m.goalZ
	}
	return float64(wp.X) + 0.5, waypointTargetY(m.collisions, wp, cfg), float64(wp.Z) + 0.5
}

// waypointReached reports whether the current waypoint is within reach.
func waypointReached(wp PathNode, isLast bool, horizDist, dy float64) bool {
	threshold := 0.35
	vertThreshold := collisions.Epsilon
	if isLast {
		threshold = 0.3
	}
	if wp.Jump {
		threshold = 0.8
		vertThreshold = collisions.Epsilon
	}
	return horizDist < threshold && math.Abs(dy) < vertThreshold
}

// advanceWaypoint moves to the next waypoint, resetting per-waypoint state;
// returns false when the path is exhausted (navigation complete).
func (m *Module) advanceWaypoint() bool {
	m.pathIndex++
	if m.pathIndex >= len(m.path) {
		m.completeNavigation(true)
		return false
	}
	m.stuckTicks = 0
	m.retreatTicks = 0
	m.retreatCycles = 0
	m.bestWaypointDistance = math.Inf(1)
	m.doorWaitTicks = 0
	m.doorOpened = false
	return true
}

// handleDoor interacts with a door waypoint when close enough; returns true
// when the tick should stop to wait for the door.
func (m *Module) handleDoor(wp PathNode, x, z float64) bool {
	if !wp.InteractDoor || m.doorOpened {
		return false
	}
	doorDist := math.Sqrt(
		math.Pow(float64(wp.DoorX)+0.5-x, 2) +
			math.Pow(float64(wp.DoorZ)+0.5-z, 2),
	)
	if doorDist < 2.5 {
		// look at the door block
		m.self.LookAt(float64(wp.DoorX)+0.5, float64(wp.DoorY)+0.5, float64(wp.DoorZ)+0.5)
		// right-click the door
		if err := m.physics.QueuePacketContext(m.navigationContext, &packets.C2SUseItemOn{
			Location: ns.Position{X: wp.DoorX, Y: wp.DoorY, Z: wp.DoorZ},
			Face:     0, CursorPositionX: 0.5, CursorPositionY: 0.5, CursorPositionZ: 0.5,
			Sequence: ns.VarInt(m.client.NextBISequence()),
		}); err != nil {
			m.completeNavigation(false)
			return true
		}
		m.doorOpened = true
		m.doorWaitTicks = 4 // wait a few ticks for the server to process
		m.physics.SetInput(0, 0, false)
		return true
	}
	return false
}

// lookTarget resolves the horizontal look direction, applying wall-slide and
// retreat logic on collisions.
func (m *Module) lookTarget(ascending bool, x, z, dx, dz, wpX, wpZ float64) (lookX, lookZ float64) {
	p := m.physics
	lookX, lookZ = wpX, wpZ
	switch {
	case ascending:
		// press straight into the block to climb it — never wall-slide here,
		// the horizontal collision IS the block we're jumping onto.
	case m.retreatTicks > 0:
		lookX = x - dx
		lookZ = z - dz
		m.retreatTicks--
	case p.HasHorizontalCollision() && m.stuckTicks > 3:
		m.retreatTicks = 8
		m.retreatCycles++
		lookX = x - dx
		lookZ = z - dz
	case p.HasHorizontalCollision():
		xCol, zCol := p.CollisionAxes()
		if xCol && zCol {
			m.retreatTicks = 8
			m.retreatCycles++
			lookX = x - dx
			lookZ = z - dz
		} else if xCol {
			lookX = x
			lookZ = z + dz
		} else {
			lookX = x + dx
			lookZ = z
		}
	}
	return lookX, lookZ
}

// selectMovementInput decides sneak/sprint/jump state for the current waypoint.
func (m *Module) selectMovementInput(wp PathNode, x, z, horizDist float64) (sneaking, sprinting, jumping bool) {
	p := m.physics
	sneaking = m.savedSneaking || wp.Sneaking
	if wp.Jump {
		sprinting = true
		sneaking = false
		// The shared trajectory takes off at the source waypoint, not its edge.
		jumping = p.IsOnGround()
	} else if !sneaking && horizDist > 2.0 {
		// sprint when moving straight and far enough ahead
		sprinting = shouldSprint(m.path, m.pathIndex, x, z)
	}
	return sneaking, sprinting, jumping
}

// detectStuck measures progress toward the waypoint, not sideways movement or
// retreat distance. A wall slide or repeated correction must not reset the clock.
func (m *Module) detectStuck(distance float64) {
	if distance < m.bestWaypointDistance-0.01 {
		m.bestWaypointDistance = distance
		m.stuckTicks = 0
	} else {
		m.stuckTicks++
	}
	if m.stuckTicks > 40 || m.retreatCycles > 3 {
		if m.tryRepath() {
			return
		}
		m.completeNavigation(false)
	}
}

// tryRepath attempts to recompute a path to the current goal.
func (m *Module) tryRepath() bool {
	s := m.self
	w := m.world
	col := m.collisions
	ents := m.entities
	p := m.physics
	p.SetInput(0, 0, false)

	sx, sy, sz := s.Position()
	startX := int(math.Floor(sx))
	startY := int(math.Floor(sy))
	startZ := int(math.Floor(sz))

	gx := int(math.Floor(m.goalX))
	gy := int(math.Floor(m.goalY))
	gz := int(math.Floor(m.goalZ))

	maxNodes := m.MaxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}

	cfg := m.simulationConfig()

	path, err := findPath(m.navigationContext, w, col, ents, startX, startY, startZ, gx, gy, gz, maxNodes, cfg)
	if err != nil {
		return false
	}
	for _, node := range path {
		if !nodeLoaded(w, node.X, node.Y, node.Z, cfg) {
			return false
		}
	}
	// Repeating the same remaining route cannot resolve a rejected edge.
	// Stop safely instead of resetting the stuck clock forever.
	remaining := m.path[m.pathIndex:]
	if len(path) > 0 && len(remaining) > 0 && path[0].X == startX && path[0].Y == startY && path[0].Z == startZ {
		path = path[1:]
	}
	if len(path) == 0 || sameRoute(path, remaining) {
		return false
	}

	m.path = path
	m.pathIndex = 0
	m.stuckTicks = 0
	m.retreatTicks = 0
	m.retreatCycles = 0
	m.bestWaypointDistance = math.Inf(1)
	m.doorWaitTicks = 0
	m.doorOpened = false
	return true
}

func sameRoute(a, b []PathNode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].X != b[i].X || a[i].Y != b[i].Y || a[i].Z != b[i].Z ||
			a[i].Jump != b[i].Jump || a[i].Sneaking != b[i].Sneaking ||
			a[i].InteractDoor != b[i].InteractDoor {
			return false
		}
	}
	return true
}

func (m *Module) completeNavigation(reached bool) {
	if m.cancel != nil {
		m.cancel()
	}
	m.navigating = false
	m.path = nil

	m.physics.SetInput(0, 0, false)
	m.self.SetSprinting(m.savedSprinting)
	m.self.SetSneaking(m.savedSneaking)

	for _, cb := range m.onNavigationComplete {
		cb(reached)
	}
}

// waypointTargetY returns the feet height the bot should aim for at a waypoint,
// resolving partial-block support surfaces (slabs, stairs, snow) instead of the
// integer block floor.
func waypointTargetY(col *collisions.Module, wp PathNode, cfg simulationConfig) float64 {
	if col != nil {
		return nodeSurfaceY(col, wp.X, wp.Y, wp.Z, cfg)
	}
	return float64(wp.Y)
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

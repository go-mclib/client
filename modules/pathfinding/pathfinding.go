package pathfinding

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

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

// Module follows collision-validated, bounded terrain segments. All controller
// state is guarded by mu; the expensive planner never runs in a physics callback.
type Module struct {
	client     *client.Client
	self       *self.Module
	world      *world.Module
	collisions *collisions.Module
	physics    *physics.Module
	entities   *entities.Module
	MaxNodes   int

	mu                            sync.Mutex
	navigating                    bool
	generation                    uint64
	navigationContext             context.Context
	cancel                        context.CancelFunc
	planCancel                    context.CancelFunc
	planID                        uint64
	planning                      bool
	path                          []PathNode
	pathIndex                     int
	goalX, goalY, goalZ           float64
	segmentFinal                  bool
	stuckTicks                    int
	bestWaypointDistance          float64
	jumpStarted                   bool
	jumpAirborne                  bool
	doorWaitTicks                 int
	doorOpened                    bool
	savedSprinting, savedSneaking bool
	blockedEdges                  map[[6]int]struct{}
	frontiers                     map[[3]int]int
	failures                      int
	waitingSince                  time.Time
	waitingForTerrain             bool
	nextPlan                      time.Time
	plannedRevision               uint64
	terrainRevision               atomic.Uint64
	corrected                     atomic.Bool
	onPathFound                   []func(path []PathNode)
	onNavigationComplete          []func(reached bool)
}

func New() *Module                              { return &Module{MaxNodes: DefaultMaxNodes} }
func (m *Module) Name() string                  { return ModuleName }
func (m *Module) HandlePacket(_ *jp.WirePacket) {}
func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
}
func (m *Module) Wire() error {
	m.self, m.world = self.From(m.client), world.From(m.client)
	m.collisions, m.physics = collisions.From(m.client), physics.From(m.client)
	if m.self == nil || m.world == nil || m.collisions == nil || m.physics == nil {
		return fmt.Errorf("requires self, world, collisions and physics modules")
	}
	m.entities = entities.From(m.client)
	m.physics.OnTick(m.navigationTick)
	m.self.OnRespawn(m.Reset)
	m.self.OnDeath(m.Reset)
	m.self.OnDimensionChange(func(string) { m.Reset() })
	m.self.OnPosition(func(float64, float64, float64) { m.corrected.Store(true) })
	// World callbacks may run under world/packet locks: only publish an epoch.
	m.world.OnChunkLoad(func(int32, int32) { m.terrainRevision.Add(1) })
	m.world.OnChunkUnload(func(int32, int32) { m.terrainRevision.Add(1) })
	m.world.OnBlockUpdate(func(int, int, int, int32) { m.terrainRevision.Add(1) })
	return nil
}
func (m *Module) Reset() { m.Stop() }
func From(c *client.Client) *Module {
	if mod := c.Module(ModuleName); mod != nil {
		return mod.(*Module)
	}
	return nil
}
func (m *Module) OnPathFound(cb func(path []PathNode)) { m.onPathFound = append(m.onPathFound, cb) }
func (m *Module) OnNavigationComplete(cb func(reached bool)) {
	m.onNavigationComplete = append(m.onNavigationComplete, cb)
}

func validGoal(x, y, z float64) bool {
	return !math.IsNaN(x) && !math.IsNaN(y) && !math.IsNaN(z) &&
		!math.IsInf(x, 0) && !math.IsInf(y, 0) && !math.IsInf(z, 0) &&
		math.Abs(x) < 30000000 && math.Abs(z) < 30000000 && math.Abs(y) < 20000000
}

// FindPath returns a complete route in currently loaded terrain; unlike
// NavigateTo it does not stream successive segments or return a partial route.
func (m *Module) FindPath(x, y, z float64) ([]PathNode, error) {
	return m.FindPathContext(context.Background(), x, y, z)
}
func (m *Module) FindPathContext(ctx context.Context, x, y, z float64) ([]PathNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validGoal(x, y, z) {
		return nil, fmt.Errorf("pathfinding: invalid goal")
	}
	cfg := m.simulationConfig()
	gx, gy, gz := int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z))
	if err := m.validateGoal(x, y, z, cfg); err != nil {
		return nil, err
	}
	sx, sy, sz := m.self.Position()
	path, err := findPath(ctx, m.world, m.collisions, m.entities,
		int(math.Floor(sx)), int(math.Floor(sy)), int(math.Floor(sz)), gx, gy, gz, m.nodeLimit(), cfg)
	if err != nil {
		return nil, err
	}
	for _, n := range path {
		if !nodeLoaded(m.world, n.X, n.Y, n.Z, cfg) {
			return nil, fmt.Errorf("pathfinding: route terrain unloaded")
		}
	}
	for _, cb := range m.onPathFound {
		cb(path)
	}
	return path, nil
}
func (m *Module) nodeLimit() int {
	if m.MaxNodes > 0 {
		return m.MaxNodes
	}
	return DefaultMaxNodes
}
func (m *Module) validateGoal(x, y, z float64, cfg simulationConfig) error {
	gx, gy, gz := int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z))
	if !nodeLoaded(m.world, gx, gy, gz, cfg) {
		return fmt.Errorf("pathfinding: goal terrain is unloaded")
	}
	fy, ok := standSurface(m.collisions, gx, gy, gz, cfg.sneakingHeight, cfg)
	if !ok || !terrainLoaded(m.world, x, fy, z, cfg.height, cfg) ||
		!m.collisions.CanFitAt(x, fy, z, cfg.width, cfg.sneakingHeight) ||
		!m.collisions.IsOnGround(x, fy, z, cfg.width) {
		return fmt.Errorf("pathfinding: goal has no support or headroom")
	}
	return nil
}

func (m *Module) NavigateTo(x, y, z float64) error {
	return m.NavigateToContext(context.Background(), x, y, z)
}

// NavigateToContext starts a cancellable navigation, including for an unloaded
// destination. Acceptance is not arrival: planning and frontier waiting keep
// IsNavigating true, and only reaching the ultimate goal reports success.
func (m *Module) NavigateToContext(parent context.Context, x, y, z float64) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if !validGoal(x, y, z) {
		return fmt.Errorf("pathfinding: invalid goal")
	}
	cfg := m.simulationConfig()
	if nodeLoaded(m.world, int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z)), cfg) {
		if err := m.validateGoal(x, y, z, cfg); err != nil {
			return err
		}
	}
	m.Stop()
	m.mu.Lock()
	ctx, cancel := context.WithCancel(parent)
	m.navigationContext, m.cancel = ctx, cancel
	generation := m.generation
	m.navigating = true
	m.goalX, m.goalY, m.goalZ = x, y, z
	m.savedSprinting, m.savedSneaking = m.self.Sprinting(), m.self.Sneaking()
	m.blockedEdges, m.frontiers = make(map[[6]int]struct{}), make(map[[3]int]int)
	m.failures = 0
	m.waitingSince = time.Now()
	m.waitingForTerrain = false
	m.nextPlan = time.Time{}
	m.corrected.Store(false)
	m.requestPlan()
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

func (m *Module) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	if m.cancel != nil {
		m.cancel()
	}
	if m.planCancel != nil {
		m.planCancel()
	}
	m.planID++
	m.planning = false
	if m.navigating {
		m.physics.SetInput(0, 0, false)
		m.self.SetSprinting(m.savedSprinting)
		m.self.SetSneaking(m.savedSneaking)
	}
	m.navigating = false
	m.path = nil
}
func (m *Module) IsNavigating() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.navigating
}

// requestPlan is called with mu held. Copies are small and bounded; world reads,
// jump simulation and A* run outside both the navigation and physics tick locks.
func (m *Module) requestPlan() {
	if m.planning || !m.navigating || m.navigationContext.Err() != nil {
		return
	}
	m.physics.SetInput(0, 0, false)
	m.path = nil
	m.planning = true
	m.waitingForTerrain = false
	m.planID++
	id, generation := m.planID, m.generation
	ctx, cancel := context.WithCancel(m.navigationContext)
	m.planCancel = cancel
	sx, sy, sz := m.self.Position()
	gx, gy, gz := m.goalX, m.goalY, m.goalZ
	cfg := m.simulationConfig()
	cfg.blockedEdges = make(map[[6]int]struct{}, len(m.blockedEdges))
	for k := range m.blockedEdges {
		cfg.blockedEdges[k] = struct{}{}
	}
	cfg.frontiers = make(map[[3]int]int, len(m.frontiers))
	for k, v := range m.frontiers {
		cfg.frontiers[k] = v
	}
	limit, revision := m.nodeLimit(), m.terrainRevision.Load()
	go func() {
		defer cancel()
		path, err := searchPath(ctx, m.world, m.collisions, m.entities,
			int(math.Floor(sx)), int(math.Floor(sy)), int(math.Floor(sz)),
			int(math.Floor(gx)), int(math.Floor(gy)), int(math.Floor(gz)), limit, cfg, true)
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.navigating || generation != m.generation || id != m.planID || ctx.Err() != nil {
			return
		}
		m.planning = false
		m.plannedRevision = revision
		m.nextPlan = time.Now().Add(500 * time.Millisecond)
		if err != nil || len(path) == 0 {
			return
		}
		end := path[len(path)-1]
		m.segmentFinal = end.X == int(math.Floor(gx)) && end.Y == int(math.Floor(gy)) && end.Z == int(math.Floor(gz))
		if m.segmentFinal && m.validateGoal(gx, gy, gz, cfg) != nil {
			m.completeNavigation(false)
			return
		}
		m.path, m.pathIndex = path, 0
		m.resetWaypoint()
		// Notifications are not made by the physics loop, and may call Stop.
		callbacks := append([]func([]PathNode){}, m.onPathFound...)
		if len(callbacks) > 0 {
			notice := append([]PathNode(nil), path...)
			m.mu.Unlock()
			for _, cb := range callbacks {
				cb(notice)
			}
			m.mu.Lock()
		}
	}()
}

func (m *Module) navigationTick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.navigating {
		return
	}
	if m.navigationContext.Err() != nil {
		m.completeNavigation(false)
		return
	}
	if m.corrected.Swap(false) {
		// A server teleport invalidates the planner's start and in-flight jump.
		m.invalidatePlan()
		m.failures++
		if m.failures > 8 {
			m.completeNavigation(false)
			return
		}
	}
	x, y, z := m.self.Position()
	cfg := m.simulationConfig()
	if !navigationLoaded(m.world, x, y, z, cfg) {
		m.physics.SetInput(0, 0, false)
		if len(m.path) > 0 {
			m.invalidatePlan()
		}
		m.waitOrPlan(false)
		return
	}
	if m.planning {
		return
	}
	if len(m.path) == 0 {
		m.waitOrPlan(true)
		return
	}
	if m.handleObstructions(cfg) {
		return
	}

	vx, _, vz := m.physics.Velocity()
	onGround := m.physics.IsOnGround() || m.collisions.IsOnGround(x, y, z, cfg.width)
	for {
		wp := m.path[m.pathIndex]
		if wp.InteractDoor && m.handleDoor(wp, x, z) {
			return
		}
		last := m.pathIndex == len(m.path)-1
		tx, ty, tz := m.waypointTarget(wp, last, cfg)
		distance := math.Hypot(tx-x, tz-z)
		// A final point or a jump takeoff needs settled momentum; ordinary
		// corners are passed only once the next actual-position sweep is safe.
		arrival := waypointReached(wp, last, distance, ty-y)
		if wp.Jump && onGround && m.jumpAirborne && int(math.Floor(x)) == wp.X && int(math.Floor(z)) == wp.Z && math.Abs(ty-y) < 0.08 {
			// Landing proves the jump edge, not the exact terminal destination.
			// Finish centering/braking as an ordinary waypoint without jumping again.
			m.path[m.pathIndex].Jump = false
			wp.Jump = false
			m.jumpStarted, m.jumpAirborne = false, false
			arrival = waypointReached(wp, last, distance, ty-y)
		}
		needsStop := last || m.pathIndex+1 < len(m.path) && m.path[m.pathIndex+1].Jump
		if !arrival || !onGround || needsStop && math.Hypot(vx, vz) > 0.025 {
			break
		}
		if !last && !wp.Jump && !m.path[m.pathIndex+1].Jump && !m.path[m.pathIndex+1].InteractDoor {
			next := m.path[m.pathIndex+1]
			ny := waypointTargetY(m.collisions, next, cfg)
			if math.Abs(ny-y) < 0.08 && !walkCorridor(m.world, m.collisions, x, y, z, float64(next.X)+0.5, ny, float64(next.Z)+0.5, next.Sneaking, cfg) {
				if distance < 0.08 && math.Hypot(vx, vz) < 0.025 {
					// We already reached this waypoint. It is the outgoing
					// edge that failed, not the edge that brought us here.
					m.rejectEdge(m.pathIndex + 1)
					return
				}
				break
			}
		}
		if !m.advanceWaypoint() {
			return
		}
	}
	wp := m.path[m.pathIndex]
	last := m.pathIndex == len(m.path)-1
	tx, ty, tz := m.waypointTarget(wp, last, cfg)
	if !wp.Jump && !wp.InteractDoor && math.Abs(ty-y) < 0.08 &&
		!walkCorridor(m.world, m.collisions, x, y, z, tx, ty, tz, wp.Sneaking, cfg) {
		m.rejectEdge(m.pathIndex)
		return
	}
	distance := math.Hypot(tx-x, tz-z)
	if m.handleDoor(wp, x, z) {
		return
	}

	if wp.Jump {
		m.followJump(wp, x, y, z, onGround, cfg)
		m.detectStuck(distance)
		return
	}
	// Look ahead only over ordinary, same-height supported swept corridors.
	lookahead := m.pathIndex
	for i := m.pathIndex + 1; i < len(m.path) && i <= m.pathIndex+4; i++ {
		next, prev := m.path[i], m.path[i-1]
		if next.Jump || next.InteractDoor || prev.InteractDoor || next.Sneaking != wp.Sneaking {
			break
		}
		nx, ny, nz := m.waypointTarget(next, i == len(m.path)-1, cfg)
		if math.Hypot(nx-x, nz-z) > 4 || math.Abs(ny-y) > 0.08 {
			break
		}
		if !walkCorridor(m.world, m.collisions, x, y, z, nx, ny, nz, wp.Sneaking, cfg) {
			break
		}
		tx, ty, tz = nx, ny, nz
		lookahead = i
	}
	if lookahead > m.pathIndex {
		m.pathIndex = lookahead
		m.resetWaypoint()
		wp = m.path[m.pathIndex]
		distance = math.Hypot(tx-x, tz-z)
	}
	sneaking := m.savedSneaking || wp.Sneaking
	m.self.SetSneaking(sneaking)
	steerDistance := math.Hypot(tx-x, tz-z)
	sprinting := !sneaking && steerDistance > 2.5 && math.Abs(ty-y) < 0.08
	m.self.SetSprinting(sprinting)
	friction := physics.ModifiedFriction(physics.GetBlockFrictionAt(m.world, x, y, z, cfg.width), cfg.frictionModifier)
	drag := friction * physics.ModifiedFriction(physics.AirFrictionMul, cfg.airDragModifier)
	accel := physics.GroundAcceleration(m.self.MovementSpeed(sprinting), friction) * 0.98
	if sneaking {
		accel *= m.self.AttributeValue("minecraft:sneaking_speed", 0.3)
	}
	if !onGround {
		drag, accel = physics.ModifiedFriction(physics.AirFrictionMul, cfg.airDragModifier), physics.FlyingSpeed*0.98
	}
	ux, uz := steeringInput(tx-x, tz-z, vx, vz, accel, drag)
	// Keep the view along the route rather than snapping 180 degrees to brake.
	m.self.LookAt(tx, y+cfg.height, tz)
	yaw, _ := m.self.Rotation()
	radians := float64(yaw) * math.Pi / 180
	forward := -ux*math.Sin(radians) + uz*math.Cos(radians)
	strafe := ux*math.Cos(radians) + uz*math.Sin(radians)
	// Invert LocalPlayer.modifyInput's square-to-circle length adjustment;
	// the controller already requested a world-space acceleration magnitude.
	if length := math.Hypot(forward, strafe); length > 0 {
		factor := math.Max(math.Abs(forward), math.Abs(strafe)) / length
		forward, strafe = forward*factor, strafe*factor
	}
	m.physics.SetInput(forward, strafe, false)
	m.detectStuck(distance)
}

// steeringInput controls desired displacement, subtracting carried velocity.
// Minecraft adds input before moving and applies drag afterward. Ignoring that
// order causes endpoint overshoot and repeated center-to-center reversals.
func steeringInput(dx, dz, vx, vz, acceleration, drag float64) (float64, float64) {
	distance := math.Hypot(dx, dz)
	if acceleration <= 0 {
		return 0, 0
	}
	maxSpeed := math.Min(0.28, acceleration/math.Max(0.02, 1-drag))
	// A proportional approach is critically damped by the velocity subtraction;
	speed := math.Min(maxSpeed, math.Min(distance*0.45, math.Sqrt(2*acceleration*distance)*0.65))
	wantX, wantZ := 0.0, 0.0
	if distance > 1e-8 {
		wantX, wantZ = dx/distance*speed, dz/distance*speed
	}
	ux, uz := (wantX-vx)/acceleration, (wantZ-vz)/acceleration
	if length := math.Hypot(ux, uz); length > 1 {
		ux, uz = ux/length, uz/length
	}
	return ux, uz
}

func (m *Module) waitOrPlan(loaded bool) {
	now := time.Now()
	if m.waitingSince.IsZero() {
		m.waitingSince = now
	}
	if now.Sub(m.waitingSince) > 30*time.Second {
		m.completeNavigation(false)
		return
	}
	if m.planning || !loaded || now.Before(m.nextPlan) {
		return
	}
	if m.waitingForTerrain && m.terrainRevision.Load() == m.plannedRevision {
		return
	}
	m.waitingForTerrain = false
	// Retry unchanged terrain at most once per second; terrain events wake a
	// waiting frontier sooner, without an unbounded tick-by-tick busy search.
	if m.terrainRevision.Load() == m.plannedRevision && now.Before(m.nextPlan.Add(500*time.Millisecond)) {
		return
	}
	m.requestPlan()
}
func (m *Module) invalidatePlan() {
	if m.planCancel != nil {
		m.planCancel()
	}
	m.planID++
	m.planning = false
	m.path = nil
	m.physics.SetInput(0, 0, false)
	m.waitingForTerrain = false
	m.nextPlan = time.Time{}
}
func (m *Module) resetWaypoint() {
	m.stuckTicks = 0
	m.bestWaypointDistance = math.Inf(1)
	m.doorWaitTicks, m.doorOpened = 0, false
	m.jumpStarted, m.jumpAirborne = false, false
}
func (m *Module) advanceWaypoint() bool {
	m.pathIndex++
	m.waitingSince = time.Now()
	if m.pathIndex == len(m.path) {
		m.failures = 0
		if len(m.blockedEdges) > 128 {
			clear(m.blockedEdges)
		}
		if m.segmentFinal {
			m.completeNavigation(true)
		} else {
			end := m.path[len(m.path)-1]
			// Keep history bounded on very long journeys.
			if len(m.frontiers) >= 128 {
				clear(m.frontiers)
			}
			m.frontiers[[3]int{end.X, end.Y, end.Z}]++
			m.path = nil
			m.physics.SetInput(0, 0, false)
			cfg := m.simulationConfig()
			for _, off := range cardinalOffsets {
				if !navigationLoaded(m.world, float64(end.X+off[0])+0.5, waypointTargetY(m.collisions, end, cfg), float64(end.Z+off[1])+0.5, cfg) {
					m.waitingForTerrain = !nodeLoaded(m.world, int(math.Floor(m.goalX)), int(math.Floor(m.goalY)), int(math.Floor(m.goalZ)), cfg)
					break
				}
			}
			if !m.waitingForTerrain || m.terrainRevision.Load() != m.plannedRevision {
				m.requestPlan()
			}
		}
		return false
	}
	m.resetWaypoint()
	return true
}
func (m *Module) waypointTarget(wp PathNode, last bool, cfg simulationConfig) (float64, float64, float64) {
	if last && m.segmentFinal {
		return m.goalX, waypointTargetY(m.collisions, wp, cfg), m.goalZ
	}
	return float64(wp.X) + 0.5, waypointTargetY(m.collisions, wp, cfg), float64(wp.Z) + 0.5
}
func waypointReached(wp PathNode, last bool, distance, dy float64) bool {
	tolerance := 0.3
	if last {
		tolerance = 0.12
	}
	return distance < tolerance && math.Abs(dy) < 0.08
}
func waypointTargetY(col *collisions.Module, wp PathNode, cfg simulationConfig) float64 {
	if col != nil {
		return nodeSurfaceY(col, wp.X, wp.Y, wp.Z, cfg)
	}
	return float64(wp.Y)
}

func (m *Module) handleObstructions(cfg simulationConfig) bool {
	for i := m.pathIndex; i < len(m.path) && i < m.pathIndex+3; i++ {
		n := m.path[i]
		if !navigationLoaded(m.world, float64(n.X)+0.5, waypointTargetY(m.collisions, n, cfg), float64(n.Z)+0.5, cfg) {
			m.invalidatePlan()
			return true
		}
		if n.InteractDoor {
			_, _, _, closed := findClosedWoodenDoor(m.world, n.DoorX, n.DoorY, n.DoorZ)
			if closed {
				continue
			}
		}
		cost, _, destY := moveCost(m.world, m.collisions, m.entities, n.X, n.Y, n.Z, cfg)
		blocked := cost < 0
		if !blocked && i > m.pathIndex && !n.Jump && !n.InteractDoor {
			prev := m.path[i-1]
			if !prev.InteractDoor {
				height := cfg.height
				if n.Sneaking {
					height = cfg.sneakingHeight
				}
				srcY := nodeSurfaceY(m.collisions, prev.X, prev.Y, prev.Z, cfg)
				blocked = !canPassBetween(m.world, m.collisions, prev.X, prev.Z, n.X, n.Z, srcY, math.Max(srcY, destY), height, cfg)
			}
		}
		if blocked {
			m.rejectEdge(i)
			return true
		}
	}
	return false
}
func (m *Module) handleDoor(wp PathNode, x, z float64) bool {
	if !wp.InteractDoor {
		return false
	}
	_, _, _, closed := findClosedWoodenDoor(m.world, wp.DoorX, wp.DoorY, wp.DoorZ)
	if !closed {
		return false
	}
	if math.Hypot(float64(wp.DoorX)+0.5-x, float64(wp.DoorZ)+0.5-z) >= 2.5 {
		return false
	}
	m.physics.SetInput(0, 0, false)
	if m.doorOpened {
		m.doorWaitTicks++
		if m.doorWaitTicks >= 20 {
			m.rejectEdge(m.pathIndex)
		}
		return true
	}
	m.self.LookAt(float64(wp.DoorX)+0.5, float64(wp.DoorY)+0.5, float64(wp.DoorZ)+0.5)
	if err := m.physics.QueuePacketContext(m.navigationContext, &packets.C2SUseItemOn{
		Location: ns.Position{X: wp.DoorX, Y: wp.DoorY, Z: wp.DoorZ},
		Face:     0, CursorPositionX: 0.5, CursorPositionY: 0.5, CursorPositionZ: 0.5,
		Sequence: ns.VarInt(m.client.NextBISequence()),
	}); err != nil {
		m.completeNavigation(false)
		return true
	}
	m.doorOpened = true
	return true
}
func (m *Module) followJump(wp PathNode, x, y, z float64, onGround bool, cfg simulationConfig) {
	if !m.jumpStarted {
		// Re-simulate from the actual settled takeoff, not the nominal center.
		vx, _, vz := m.physics.Velocity()
		if math.Hypot(vx, vz) > 0.025 {
			m.physics.SetInput(0, 0, false)
			return
		}
		landing, ok := simulateOneJump(m.navigationContext, m.collisions, m.world, x, y, z, wp.JumpYaw, cfg)
		if !onGround || !ok || landing.X != wp.X || landing.Y != wp.Y || landing.Z != wp.Z {
			m.rejectEdge(m.pathIndex)
			return
		}
		m.jumpStarted = true
		m.self.SetSneaking(false)
		m.self.SetSprinting(true)
		m.self.LookAt(x-math.Sin(wp.JumpYaw*math.Pi/180), y+cfg.height, z+math.Cos(wp.JumpYaw*math.Pi/180))
		m.physics.SetInput(1, 0, true)
		return
	}
	if !onGround {
		m.jumpAirborne = true
	}
	if m.jumpAirborne && onGround {
		m.rejectEdge(m.pathIndex)
		return
	}
	// Exactly one jump press; preserve the validated yaw and forward trajectory
	// until landing, instead of homing at the landing center in midair.
	m.self.SetSneaking(false)
	m.self.SetSprinting(true)
	m.physics.SetInput(1, 0, false)
}
func (m *Module) detectStuck(distance float64) {
	if distance < m.bestWaypointDistance-0.01 {
		m.bestWaypointDistance = distance
		m.stuckTicks = 0
	} else {
		m.stuckTicks++
	}
	if m.stuckTicks > 40 {
		m.rejectEdge(m.pathIndex)
	}
}
func (m *Module) rejectEdge(index int) {
	m.failures++
	if index < len(m.path) {
		to := m.path[index]
		x, y, z := m.self.Position()
		from := PathNode{X: int(math.Floor(x)), Y: int(math.Floor(y)), Z: int(math.Floor(z))}
		if index > 0 {
			from = m.path[index-1]
		}
		if m.blockedEdges == nil {
			m.blockedEdges = make(map[[6]int]struct{})
		}
		m.blockedEdges[[6]int{from.X, from.Y, from.Z, to.X, to.Y, to.Z}] = struct{}{}
	}
	m.invalidatePlan()
	if m.failures >= 8 {
		m.completeNavigation(false)
		return
	}
	m.nextPlan = time.Now().Add(250 * time.Millisecond)
}
func (m *Module) completeNavigation(reached bool) {
	if !m.navigating {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	if m.planCancel != nil {
		m.planCancel()
	}
	m.navigating, m.planning = false, false
	m.path = nil
	m.physics.SetInput(0, 0, false)
	m.self.SetSprinting(m.savedSprinting)
	m.self.SetSneaking(m.savedSneaking)
	// Callbacks must be able to call IsNavigating/Stop without deadlocking.
	callbacks := append([]func(bool){}, m.onNavigationComplete...)
	m.mu.Unlock()
	for _, cb := range callbacks {
		cb(reached)
	}
	m.mu.Lock()
}

// Package command_block_core programs and maintains a server-side cube of
// command blocks (a "core") that the client dispatches commands through via
// packets, bypassing chat. Placement is driven entirely by command blocks (no
// chat commands are ever sent): the bot locally places auto:1b command blocks
// that forceload, summon a chunk-loading pearl, and /fill the cube, then the
// cube is programmed over the wire.
//
// A core is a Size³ cube whose top is at Config.Origin (chunk-aligned in X/Z)
// and which grows downward. Stack multiple cores in one chunk by giving each a
// lower Origin.Y (no gap needed); they share one pearl via an idempotent
// keep-alive.
package command_block_core

import (
	"cmp"
	"errors"
	"sync"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/client/modules/registry"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	jp "github.com/go-mclib/protocol/javaproto"
)

const ModuleName = "command_block_core"

// hotbar slot 0 as a container slot index (used for temp-block placement).
const bootstrapSlot = 36

// pearl placement when PearlY is auto-detected: this many blocks above the
// dimension build limit, falling back to fallbackPearlY without a registry.
const (
	pearlOffsetAboveBuild = 10
	fallbackPearlY        = 330
)

// command block modes — ServerboundSetCommandBlockPacket mode ordinals.
// modeChain (SEQUENCE=0) is added when chain dispatch lands.
const (
	modeAuto    = 1 // AUTO -> repeating_command_block
	modeImpulse = 2 // REDSTONE -> command_block
)

// flagAutomatic is the "always active" command-block flag; toggling it
// false->true fires an impulse block (see dispatch.go).
const flagAutomatic = 0x04

// ForceloadMethod selects how the core's chunk is kept loaded.
type ForceloadMethod int

const (
	// ForceloadPearl keeps the chunk loaded with a NoGravity ender pearl that a
	// reserved repeating block re-summons if killed. Survives /forceload bans.
	ForceloadPearl ForceloadMethod = iota
	// ForceloadCommand uses a persistent /forceload add.
	ForceloadCommand
	// ForceloadNone relies on the bot staying within render distance.
	ForceloadNone
)

// State is the core lifecycle state.
type State int

const (
	StateIdle State = iota
	StateBuilding
	StateReady
	StateTearingDown
	StateError
)

func (s State) String() string {
	switch s {
	case StateBuilding:
		return "building"
	case StateReady:
		return "ready"
	case StateTearingDown:
		return "tearing-down"
	case StateError:
		return "error"
	default:
		return "idle"
	}
}

// Pos is a block position.
type Pos struct{ X, Y, Z int }

// Config describes where and how the core is built.
type Config struct {
	Origin Pos // cube top corner; X/Z snapped to the chunk; cube grows -Y/+X/+Z
	Size   int // cube edge (1..16); default 16

	Forceload ForceloadMethod
	PearlTag  string // shared across stacked cores
	PearlY    int    // 0 = auto-detect (build height + offset)

	CommandsPerTick int           // impulse dispatches programmed per flush
	TickInterval    time.Duration // flush cadence (≈ one game tick)
}

type Module struct {
	client    *client.Client
	self      *self.Module
	inventory *inventory.Module
	registry  *registry.Module // optional: auto-detect PearlY
	world     *world.Module    // optional: find a placement spot

	cfg Config

	mu           sync.Mutex
	state        State
	queue        []string
	dispatch     []Pos // impulse slots (the dispatch region)
	reserved     []Pos // repeating slots (the floor)
	reservedCmds []string
	nextSlot     int
	cancel       chan struct{}
}

// New creates a core module. Zero-valued config fields take sensible defaults.
func New(cfg Config) *Module {
	if cfg.Size <= 0 || cfg.Size > 16 {
		cfg.Size = 16
	}
	cfg.PearlTag = cmp.Or(cfg.PearlTag, "vc_fl")
	if cfg.CommandsPerTick <= 0 {
		cfg.CommandsPerTick = 512
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 50 * time.Millisecond
	}
	return &Module{cfg: cfg, state: StateIdle}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
	c.OnDisconnect(m.Reset)
}

func (m *Module) Wire() error {
	if m.self = self.From(m.client); m.self == nil {
		return errors.New("requires self module")
	}
	if m.inventory = inventory.From(m.client); m.inventory == nil {
		return errors.New("requires inventory module")
	}
	m.registry = registry.From(m.client)
	m.world = world.From(m.client)
	return nil
}

func (m *Module) HandlePacket(_ *jp.WirePacket) {
	// TODO: watch S2CSystemChat for advMode.notAllowed / setCommand.disabled.
}

// Reset stops dispatch and clears volatile state (called on disconnect/transfer).
func (m *Module) Reset() {
	m.mu.Lock()
	if m.cancel != nil {
		close(m.cancel)
		m.cancel = nil
	}
	m.state = StateIdle
	m.queue = nil
	m.nextSlot = 0
	m.mu.Unlock()
}

// State returns the current lifecycle state.
func (m *Module) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Module) setState(s State) {
	m.mu.Lock()
	m.state = s
	m.mu.Unlock()
}

// From retrieves the core module from a client.
func From(c *client.Client) *Module { return client.ModuleAs[*Module](c, ModuleName) }

// SetOrigin overrides the cube's top origin before Build — e.g. a position
// computed at runtime once the bot has spawned.
func (m *Module) SetOrigin(p Pos) {
	m.mu.Lock()
	m.cfg.Origin = p
	m.mu.Unlock()
}

// resolvePearlY returns PearlY above the dimension build limit, or a fallback.
func (m *Module) resolvePearlY() int {
	if m.registry != nil {
		if dt, ok := m.registry.CurrentDimensionType(); ok {
			return int(dt.MaxY()) + pearlOffsetAboveBuild
		}
	}
	return fallbackPearlY
}

// cubeBounds returns the cube's [bottom, top] world Y. The cube grows away from
// the nearer world boundary — up from Origin.Y when it's in the lower half of
// the world, down when in the upper half — so a low origin doesn't fall through
// the floor (and a high one doesn't poke through the ceiling).
func (m *Module) cubeBounds() (bottom, top int) {
	minY, maxY := m.registry.CurrentBounds()
	if m.cfg.Origin.Y < (minY+maxY)/2 {
		return m.cfg.Origin.Y, m.cfg.Origin.Y + m.cfg.Size - 1 // grow up
	}
	return m.cfg.Origin.Y - m.cfg.Size + 1, m.cfg.Origin.Y // grow down
}

// chunkFloor snaps a block coordinate down to its chunk corner (multiple of 16).
func chunkFloor(v int) int { return (v >> 4) << 4 }

// computeLayout fills the reserved (bottom floor) and dispatch (above) slots.
// The cube top is at Origin.Y and grows down to Origin.Y-Size+1.
func (m *Module) computeLayout() {
	x0, z0 := chunkFloor(m.cfg.Origin.X), chunkFloor(m.cfg.Origin.Z)
	bottom, top := m.cubeBounds()
	n := m.cfg.Size

	m.reserved = m.reserved[:0]
	for dx := range n {
		for dz := range n {
			m.reserved = append(m.reserved, Pos{x0 + dx, bottom, z0 + dz})
		}
	}

	m.dispatch = m.dispatch[:0]
	for y := bottom + 1; y <= top; y++ {
		for dx := range n {
			for dz := range n {
				m.dispatch = append(m.dispatch, Pos{x0 + dx, y, z0 + dz})
			}
		}
	}
}

package self

import (
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

const (
	ModuleName = "self"
	// EyeHeight is the unscaled standing eye height. CurrentEyeHeight and
	// EyePosition account for pose and scale.
	EyeHeight = 1.62
)

type Module struct {
	client *client.Client
	mu     sync.RWMutex

	autoRespawn bool

	// login is the last S2CLogin, patched by respawn and game-mode changes so
	// it always describes the current spawn (see LoginPacket).
	login packets.S2CLogin

	vitals     Vitals
	experience Experience
	difficulty Difficulty
	abilities  Abilities
	time       WorldTime

	// position & rotation
	x, y, z float64
	yaw     float32
	pitch   float32

	// spawn position
	spawnDimension string
	spawnPosition  ns.Position
	spawnYaw       float32
	spawnPitch     float32

	// op level (0-4, derived from entity event status 24-28)
	opLevel int8

	// when true, an external controller (e.g. pproxy) handles teleport confirms
	suppressPositionEcho bool

	// movement state flags
	sprinting   bool
	sneaking    bool
	entityFlags byte
	livingFlags byte
	noGravity   bool
	sleeping    bool
	isVehicle   bool
	vehicleID   int32

	// eye height of the current pose published by physics; 0 when unknown
	eyeHeight float64

	attributes map[string]Attribute

	effectsMu     sync.Mutex
	activeEffects map[int32]*effectInstance

	onDeath            []func()
	onSpawn            []func()
	onRespawn          []func()
	onHealthSet        []func(health, food float32)
	onPosition         []func(x, y, z float64)
	onTeleport         []func(packet *packets.S2CPlayerPosition, oldYaw, oldPitch float32)
	onGameEvent        []func(event uint8, value float32)
	onGamemodeChange   []func(gamemode uint8)
	onDimensionChange  []func(dimensionName string)
	onEffectAdded      []func(effectID, amplifier, duration int32)
	onEffectRemoved    []func(effectID int32)
	onDifficultyChange []func(difficulty uint8, locked bool)
	onAbilitiesChange  []func(flags int8, flySpeed, fovMod float32)
	onTimeUpdate       []func(worldAge, timeOfDay int64)
	onExperienceChange []func(bar float32, level, total int32)
	onAttributeUpdate  []func(name string, value float64)
	onVehicleChange    []func(previous, current int32)
}

func New() *Module {
	m := &Module{
		autoRespawn:   true,
		activeEffects: make(map[int32]*effectInstance),
		attributes:    make(map[string]Attribute),
	}
	m.Reset()
	return m
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)

	// Clear chunks on dimension change/respawn. This is registered in Init,
	// before any Wire-time OnRespawn subscriber, on purpose: consumers such as
	// pproxy start polling for the new dimension's chunks from their OnRespawn
	// callback and must not observe the previous dimension's terrain. It cannot
	// move to world.Wire for that reason. Entities clear their own instances on
	// S2CRespawn (keeping scoreboard teams, which vanilla does not re-send).
	m.OnRespawn(func() {
		if w := c.Module("world"); w != nil {
			type chunkClearer interface{ ClearChunks() }
			if cc, ok := w.(chunkClearer); ok {
				cc.ClearChunks()
			}
		}
	})
}

// Vanilla defaults of a freshly spawned player.
var (
	defaultVitals    = Vitals{Health: 20, Food: 20, FoodSaturation: 5}
	defaultAbilities = Abilities{FlyingSpeed: 0.05, FOVModifier: 0.1}
)

func (m *Module) Reset() {
	m.mu.Lock()
	m.login = packets.S2CLogin{}
	m.vitals, m.experience = defaultVitals, Experience{}
	m.difficulty, m.abilities, m.time = Difficulty{}, defaultAbilities, WorldTime{}
	m.x, m.y, m.z, m.yaw, m.pitch = 0, 0, 0, 0, 0
	m.resetEntityStateLocked()
	m.spawnDimension, m.spawnPosition, m.spawnYaw, m.spawnPitch = "", ns.Position{}, 0, 0
	m.opLevel = 0
	clear(m.attributes)
	m.mu.Unlock()
	m.effectsMu.Lock()
	clear(m.activeEffects)
	m.effectsMu.Unlock()
}

// resetEntityStateLocked clears synchronized entity state that a new player
// entity (respawn, dimension change, transfer) does not inherit. Reset and
// respawn do not fire OnVehicleChange; respawn reports a dismount itself.
func (m *Module) resetEntityStateLocked() {
	m.sprinting, m.sneaking = false, false
	m.entityFlags, m.livingFlags = 0, 0
	m.noGravity, m.sleeping, m.isVehicle = false, false, false
	m.vehicleID = 0
	m.eyeHeight = 0
}

// From retrieves the self module from a client.
func From(c *client.Client) *Module { return client.ModuleAs[*Module](c, ModuleName) }

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packetids.S2CLoginID:
		m.handleLogin(pkt)
	case packetids.S2CSetHealthID:
		m.handleSetHealth(pkt)
	case packetids.S2CSetExperienceID:
		m.handleSetExperience(pkt)
	case packetids.S2CPlayerPositionID:
		m.handlePlayerPosition(pkt)
	case packetids.S2CPlayerCombatKillID:
		m.handleCombatKill(pkt)
	case packetids.S2CGameEventID:
		m.handleGameEvent(pkt)
	case packetids.S2CUpdateMobEffectID:
		m.handleUpdateMobEffect(pkt)
	case packetids.S2CRemoveMobEffectID:
		m.handleRemoveMobEffect(pkt)
	case packetids.S2CChangeDifficultyID:
		m.handleChangeDifficulty(pkt)
	case packetids.S2CPlayerAbilitiesID:
		m.handlePlayerAbilities(pkt)
	case packetids.S2CSetDefaultSpawnPositionID:
		m.handleSetDefaultSpawnPosition(pkt)
	case packetids.S2CSetTimeID:
		m.handleSetTime(pkt)
	case packetids.S2CEntityEventID:
		m.handleEntityEvent(pkt)
	case packetids.S2CRespawnID:
		m.handleRespawn(pkt)
	case packetids.S2CUpdateAttributesID:
		m.handleUpdateAttributes(pkt)
	case packetids.S2CSetEntityDataID:
		m.handleOwnMetadata(pkt)
	case packetids.S2CSetPassengersID:
		m.handlePassengers(pkt)
	}
}

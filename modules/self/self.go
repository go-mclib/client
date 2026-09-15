package self

import (
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const (
	ModuleName = "self"
	EyeHeight  = 1.62
)

type Module struct {
	client *client.Client
	mu     sync.RWMutex

	autoRespawn bool

	// login state
	entityID            int32
	isHardcore          bool
	dimensionNames      []string
	maxPlayers          int32
	viewDistance        int32
	simulationDistance  int32
	reducedDebugInfo    bool
	enableRespawnScreen bool
	doLimitedCrafting   bool
	dimensionType       int32
	dimensionName       string
	hashedSeed          int64
	gamemode            uint8
	previousGameMode    int8
	isDebug             bool
	isFlat              bool
	deathLocation       ns.PrefixedOptional[ns.GlobalPos]
	portalCooldown      int32
	seaLevel            int32
	enforcesSecureChat  bool

	// health & experience
	health          float32
	food            int32
	foodSaturation  float32
	experienceBar   float32
	level           int32
	totalExperience int32

	// position & rotation
	x, y, z float64
	yaw     float32
	pitch   float32

	// difficulty
	difficulty       uint8
	difficultyLocked bool

	// abilities
	abilityFlags int8
	flyingSpeed  float32
	fovModifier  float32

	// spawn position
	spawnDimension string
	spawnPosition  ns.Position
	spawnYaw       float32
	spawnPitch     float32

	// time
	worldAge       int64
	timeOfDay      int64
	timeIncreasing bool

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

	attributes map[string]*Attribute

	effectsMu     sync.Mutex
	activeEffects map[int32]*EffectInstance

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
}

func New() *Module {
	return &Module{
		autoRespawn:    true,
		health:         20,
		food:           20,
		foodSaturation: 5,
		flyingSpeed:    0.05,
		fovModifier:    0.1,
		activeEffects:  make(map[int32]*EffectInstance),
		attributes:     make(map[string]*Attribute),
	}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)

	// clear world and entity state on dimension change/respawn
	m.OnRespawn(func() {
		if w := c.Module("world"); w != nil {
			type chunkClearer interface{ ClearChunks() }
			if cc, ok := w.(chunkClearer); ok {
				cc.ClearChunks()
			}
		}
		if e := c.Module("entities"); e != nil {
			e.Reset()
		}
	})
}

func (m *Module) Reset() {
	m.mu.Lock()
	m.health = 20
	m.food = 20
	m.foodSaturation = 5
	m.experienceBar = 0
	m.level = 0
	m.totalExperience = 0
	m.x = 0
	m.y = 0
	m.z = 0
	m.yaw = 0
	m.pitch = 0
	m.sprinting = false
	m.sneaking = false
	m.entityFlags, m.livingFlags = 0, 0
	m.noGravity, m.sleeping, m.isVehicle = false, false, false
	m.vehicleID = 0
	m.difficulty = 0
	m.difficultyLocked = false
	m.abilityFlags = 0
	m.flyingSpeed = 0.05
	m.fovModifier = 0.1
	m.spawnDimension = ""
	m.spawnPosition = ns.Position{}
	m.spawnYaw = 0
	m.spawnPitch = 0
	m.worldAge = 0
	m.timeOfDay = 0
	m.timeIncreasing = false
	m.opLevel = 0
	clear(m.attributes)
	m.mu.Unlock()
	m.effectsMu.Lock()
	clear(m.activeEffects)
	m.effectsMu.Unlock()
}

// From retrieves the self module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packet_ids.S2CLoginID:
		m.handleLogin(pkt)
	case packet_ids.S2CSetHealthID:
		m.handleSetHealth(pkt)
	case packet_ids.S2CSetExperienceID:
		m.handleSetExperience(pkt)
	case packet_ids.S2CPlayerPositionID:
		m.handlePlayerPosition(pkt)
	case packet_ids.S2CPlayerCombatKillID:
		m.handleCombatKill(pkt)
	case packet_ids.S2CGameEventID:
		m.handleGameEvent(pkt)
	case packet_ids.S2CUpdateMobEffectID:
		m.handleUpdateMobEffect(pkt)
	case packet_ids.S2CRemoveMobEffectID:
		m.handleRemoveMobEffect(pkt)
	case packet_ids.S2CChangeDifficultyID:
		m.handleChangeDifficulty(pkt)
	case packet_ids.S2CPlayerAbilitiesID:
		m.handlePlayerAbilities(pkt)
	case packet_ids.S2CSetDefaultSpawnPositionID:
		m.handleSetDefaultSpawnPosition(pkt)
	case packet_ids.S2CSetTimeID:
		m.handleSetTime(pkt)
	case packet_ids.S2CEntityEventID:
		m.handleEntityEvent(pkt)
	case packet_ids.S2CRespawnID:
		m.handleRespawn(pkt)
	case packet_ids.S2CUpdateAttributesID:
		m.handleUpdateAttributes(pkt)
	case packet_ids.S2CSetEntityDataID:
		m.handleOwnMetadata(pkt)
	case packet_ids.S2CSetPassengersID:
		m.handlePassengers(pkt)
	}
}

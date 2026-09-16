package world

import (
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/registry"
	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/nbt"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const ModuleName = "world"

// BlockEntityData holds the type and NBT data for a block entity.
type BlockEntityData struct {
	Type int32
	Data nbt.Compound
}

type Module struct {
	client   *client.Client
	registry *registry.Module // optional, supplies per-dimension world height

	mu            sync.RWMutex
	chunks        map[int64]*chunks.ChunkColumn
	blockEntities map[[3]int]*BlockEntityData // [x,y,z] -> data
	centerChunkX  int32
	centerChunkZ  int32
	viewDistance  int32

	// border state (from S2CInitializeBorder)
	border *packets.S2CInitializeBorder

	onChunkLoad         []func(x, z int32)
	onChunkUnload       []func(x, z int32)
	onBlockUpdate       []func(x, y, z int, stateID int32)
	onViewDistChange    []func(distance int32)
	onCenterChunkChange []func(x, z int32)
}

func New() *Module {
	return &Module{
		chunks:        make(map[int64]*chunks.ChunkColumn),
		blockEntities: make(map[[3]int]*BlockEntityData),
		viewDistance:  10,
	}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
}

func (m *Module) Wire() error {
	m.registry = registry.From(m.client) // optional: dynamic world height
	return nil
}

// dimensionBounds returns the current dimension's bottom Y and section count,
// falling back to the vanilla overworld when no registry data is available.
func (m *Module) dimensionBounds() (minY, sectionCount int) {
	if m.registry != nil {
		if dt, ok := m.registry.CurrentDimensionType(); ok && dt.Height > 0 {
			return int(dt.MinY), int(dt.Height) / 16
		}
	}
	return chunks.MinY, chunks.SectionCount
}

// ClearChunks removes all loaded chunks and block entities.
// Called on respawn/dimension change.
func (m *Module) ClearChunks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks = make(map[int64]*chunks.ChunkColumn)
	m.blockEntities = make(map[[3]int]*BlockEntityData)
}

func (m *Module) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks = make(map[int64]*chunks.ChunkColumn)
	m.blockEntities = make(map[[3]int]*BlockEntityData)
	m.border = nil
}

// From retrieves the world module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

func (m *Module) OnChunkLoad(cb func(x, z int32))   { m.onChunkLoad = append(m.onChunkLoad, cb) }
func (m *Module) OnChunkUnload(cb func(x, z int32)) { m.onChunkUnload = append(m.onChunkUnload, cb) }
func (m *Module) OnBlockUpdate(cb func(x, y, z int, stateID int32)) {
	m.onBlockUpdate = append(m.onBlockUpdate, cb)
}
func (m *Module) OnViewDistanceChange(cb func(distance int32)) {
	m.onViewDistChange = append(m.onViewDistChange, cb)
}
func (m *Module) OnCenterChunkChange(cb func(x, z int32)) {
	m.onCenterChunkChange = append(m.onCenterChunkChange, cb)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packet_ids.S2CLevelChunkWithLightID:
		m.handleChunkData(pkt)
	case packet_ids.S2CForgetLevelChunkID:
		m.handleUnloadChunk(pkt)
	case packet_ids.S2CBlockUpdateID:
		m.handleBlockUpdate(pkt)
	case packet_ids.S2CSectionBlocksUpdateID:
		m.handleSectionBlocksUpdate(pkt)
	case packet_ids.S2CSetChunkCacheCenterID:
		m.handleSetChunkCacheCenter(pkt)
	case packet_ids.S2CSetChunkCacheRadiusID:
		m.handleSetChunkCacheRadius(pkt)
	case packet_ids.S2CChunkBatchFinishedID:
		m.handleChunkBatchFinished()
	case packet_ids.S2CBlockEntityDataID:
		m.handleBlockEntityData(pkt)
	case packet_ids.S2CInitializeBorderID:
		m.handleInitializeBorder(pkt)
	case packet_ids.S2CBlockChangedAckID:
		// acknowledge block prediction — the server confirms our sequence.
		// currently a no-op since we trust server state, but this prevents
		// "unhandled packet" warnings in verbose mode.
	}
}

func (m *Module) handleChunkData(pkt *jp.WirePacket) {
	var d packets.S2CLevelChunkWithLight
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Printf("failed to read chunk packet: %v", err)
		return
	}

	minY, sectionCount := m.dimensionBounds()
	column, err := chunks.ParseChunkColumn(int32(d.ChunkX), int32(d.ChunkZ), minY, sectionCount, d.ChunkData, &d.LightData)
	if err != nil {
		m.client.Logger.Printf("failed to parse chunk column at (%d, %d): %v", d.ChunkX, d.ChunkZ, err)
		return
	}

	cx, cz := int32(d.ChunkX), int32(d.ChunkZ)
	key := ChunkKey(cx, cz)
	m.mu.Lock()
	m.clearChunkBlockEntitiesLocked(cx, cz)
	m.chunks[key] = column
	// store block entities from chunk data
	for _, be := range column.BlockEntities {
		x := int(cx)*16 + be.X()
		y := int(be.Y)
		z := int(cz)*16 + be.Z()
		if c, ok := be.Data.(nbt.Compound); ok {
			m.blockEntities[[3]int{x, y, z}] = &BlockEntityData{
				Type: int32(be.Type),
				Data: cloneNBT(c).(nbt.Compound),
			}
		}
	}
	m.mu.Unlock()

	for _, cb := range m.onChunkLoad {
		cb(cx, cz)
	}
}

func (m *Module) handleUnloadChunk(pkt *jp.WirePacket) {
	var d packets.S2CForgetLevelChunk
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	cx, cz := int32(d.ChunkX), int32(d.ChunkZ)
	key := ChunkKey(cx, cz)
	m.mu.Lock()
	delete(m.chunks, key)
	m.clearChunkBlockEntitiesLocked(cx, cz)
	m.mu.Unlock()

	for _, cb := range m.onChunkUnload {
		cb(cx, cz)
	}
}

func (m *Module) handleBlockEntityData(pkt *jp.WirePacket) {
	var d packets.S2CBlockEntityData
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	key := [3]int{d.Location.X, d.Location.Y, d.Location.Z}
	m.mu.Lock()
	if d.NbtData == nil {
		delete(m.blockEntities, key)
	} else if c, ok := d.NbtData.(nbt.Compound); ok {
		m.blockEntities[key] = &BlockEntityData{
			Type: int32(d.Type),
			Data: c,
		}
	}
	m.mu.Unlock()
}

func (m *Module) handleBlockUpdate(pkt *jp.WirePacket) {
	var d packets.S2CBlockUpdate
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	bx, by, bz := int(d.Location.X), int(d.Location.Y), int(d.Location.Z)
	stateID := int32(d.BlockId)

	m.mu.Lock()
	m.updateBlockLocked(bx, by, bz, stateID)
	m.mu.Unlock()

	for _, cb := range m.onBlockUpdate {
		cb(bx, by, bz, stateID)
	}
}

func (m *Module) handleSectionBlocksUpdate(pkt *jp.WirePacket) {
	var d packets.S2CSectionBlocksUpdate
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	sectionX, sectionY, sectionZ := chunks.DecodeSectionPosition(int64(d.ChunkSectionPosition))

	m.mu.Lock()
	for _, block := range d.Blocks {
		stateID, localX, localY, localZ := chunks.DecodeBlockEntry(int64(block))
		wx := int(sectionX)*16 + localX
		wy := int(sectionY)*16 + localY
		wz := int(sectionZ)*16 + localZ
		m.updateBlockLocked(wx, wy, wz, stateID)
	}
	m.mu.Unlock()

	for _, block := range d.Blocks {
		stateID, localX, localY, localZ := chunks.DecodeBlockEntry(int64(block))
		worldX := int(sectionX)*16 + localX
		worldY := int(sectionY)*16 + localY
		worldZ := int(sectionZ)*16 + localZ
		for _, cb := range m.onBlockUpdate {
			cb(worldX, worldY, worldZ, stateID)
		}
	}
}

func (m *Module) handleSetChunkCacheCenter(pkt *jp.WirePacket) {
	var d packets.S2CSetChunkCacheCenter
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	x, z := int32(d.ChunkX), int32(d.ChunkZ)
	m.mu.Lock()
	m.centerChunkX = x
	m.centerChunkZ = z
	m.mu.Unlock()

	for _, cb := range m.onCenterChunkChange {
		cb(x, z)
	}
}

func (m *Module) handleSetChunkCacheRadius(pkt *jp.WirePacket) {
	var d packets.S2CSetChunkCacheRadius
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	dist := int32(d.ViewDistance)
	m.mu.Lock()
	m.viewDistance = dist
	m.mu.Unlock()

	for _, cb := range m.onViewDistChange {
		cb(dist)
	}
}

func (m *Module) handleChunkBatchFinished() {
	m.client.SendPacket(&packets.C2SChunkBatchReceived{
		ChunksPerTick: ns.Float32(25.0),
	})
}

func (m *Module) handleInitializeBorder(pkt *jp.WirePacket) {
	var d packets.S2CInitializeBorder
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	m.mu.Lock()
	m.border = &d
	m.mu.Unlock()
}

// GetChunks returns a snapshot of all loaded chunk columns.
func (m *Module) GetChunks() []*chunks.ChunkColumn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*chunks.ChunkColumn, 0, len(m.chunks))
	for _, col := range m.chunks {
		result = append(result, col)
	}
	return result
}

// ChunkCacheCenter returns the current center chunk coordinates.
func (m *Module) ChunkCacheCenter() (x, z int32) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.centerChunkX, m.centerChunkZ
}

// GetViewDistance returns the server-sent view distance (chunk cache radius).
func (m *Module) GetViewDistance() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.viewDistance
}

// Border returns the last received border initialization, or nil.
func (m *Module) Border() *packets.S2CInitializeBorder {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.border
}

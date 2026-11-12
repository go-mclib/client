package world

import (
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/registry"
	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/nbt"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
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
	chunks        map[int64]*chunks.Column
	blockEntities map[[3]int]*BlockEntityData // [x,y,z] -> data
	centerChunkX  int32
	centerChunkZ  int32
	viewDistance  int32

	onChunkLoad         []func(x, z int32)
	onChunkUnload       []func(x, z int32)
	onBlockUpdate       []func(x, y, z int, stateID int32)
	onTransientBlock    []func(x, y, z int, stateID int32)
	onViewDistChange    []func(distance int32)
	onCenterChunkChange []func(x, z int32)
}

func New() *Module {
	return &Module{
		chunks:        make(map[int64]*chunks.Column),
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

// dimensionBounds returns the current dimension's bottom Y and section count.
func (m *Module) dimensionBounds() (minY, sectionCount int) {
	minY, maxY := m.registry.CurrentBounds()
	return minY, (maxY - minY) / 16
}

// ClearChunks removes all loaded chunks and block entities.
// Called on respawn/dimension change.
func (m *Module) ClearChunks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks = make(map[int64]*chunks.Column)
	m.blockEntities = make(map[[3]int]*BlockEntityData)
}

func (m *Module) Reset() { m.ClearChunks() }

// From retrieves the world module from a client.
func From(c *client.Client) *Module { return client.ModuleAs[*Module](c, ModuleName) }

func (m *Module) OnChunkLoad(cb func(x, z int32))   { m.onChunkLoad = append(m.onChunkLoad, cb) }
func (m *Module) OnChunkUnload(cb func(x, z int32)) { m.onChunkUnload = append(m.onChunkUnload, cb) }
func (m *Module) OnBlockUpdate(cb func(x, y, z int, stateID int32)) {
	m.onBlockUpdate = append(m.onBlockUpdate, cb)
}

// OnTransientBlock observes temporary visual blocks, never persistent world updates.
func (m *Module) OnTransientBlock(cb func(x, y, z int, stateID int32)) {
	m.onTransientBlock = append(m.onTransientBlock, cb)
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
	case packetids.S2CAddTransientBlockID:
		var d packets.S2CAddTransientBlock
		if m.client.Decode(pkt, &d) {
			for _, cb := range m.onTransientBlock {
				cb(d.Location.X, d.Location.Y, d.Location.Z, int32(d.BlockState))
			}
		}
	case packetids.S2CLevelChunkWithLightID:
		m.handleChunkData(pkt)
	case packetids.S2CForgetLevelChunkID:
		m.handleUnloadChunk(pkt)
	case packetids.S2CBlockUpdateID:
		m.handleBlockUpdate(pkt)
	case packetids.S2CSectionBlocksUpdateID:
		m.handleSectionBlocksUpdate(pkt)
	case packetids.S2CSetChunkCacheCenterID:
		m.handleSetChunkCacheCenter(pkt)
	case packetids.S2CSetChunkCacheRadiusID:
		m.handleSetChunkCacheRadius(pkt)
	case packetids.S2CChunkBatchFinishedID:
		m.handleChunkBatchFinished()
	case packetids.S2CBlockEntityDataID:
		m.handleBlockEntityData(pkt)
	case packetids.S2CBlockChangedAckID:
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
	column, err := chunks.ParseColumn(int32(d.ChunkX), int32(d.ChunkZ), minY, sectionCount, d.ChunkData, &d.LightData)
	if err != nil {
		m.client.Logger.Printf("failed to parse chunk column at (%d, %d): %v", d.ChunkX, d.ChunkZ, err)
		return
	}

	cx, cz := int32(d.ChunkX), int32(d.ChunkZ)
	key := chunkKey(cx, cz)
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
	if !m.client.Decode(pkt, &d) {
		return
	}

	cx, cz := int32(d.ChunkX), int32(d.ChunkZ)
	key := chunkKey(cx, cz)
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
	if !m.client.Decode(pkt, &d) {
		return
	}

	key := [3]int{d.Location.X, d.Location.Y, d.Location.Z}
	m.mu.Lock()
	if d.NBTData == nil {
		delete(m.blockEntities, key)
	} else if c, ok := d.NBTData.(nbt.Compound); ok {
		m.blockEntities[key] = &BlockEntityData{
			Type: int32(d.Type),
			Data: c,
		}
	}
	m.mu.Unlock()
}

func (m *Module) handleBlockUpdate(pkt *jp.WirePacket) {
	var d packets.S2CBlockUpdate
	if !m.client.Decode(pkt, &d) {
		return
	}

	bx, by, bz := int(d.Location.X), int(d.Location.Y), int(d.Location.Z)
	stateID := int32(d.BlockID)

	m.mu.Lock()
	m.updateBlockLocked(bx, by, bz, stateID)
	m.mu.Unlock()

	for _, cb := range m.onBlockUpdate {
		cb(bx, by, bz, stateID)
	}
}

func (m *Module) handleSectionBlocksUpdate(pkt *jp.WirePacket) {
	var d packets.S2CSectionBlocksUpdate
	if !m.client.Decode(pkt, &d) {
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
	if !m.client.Decode(pkt, &d) {
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
	if !m.client.Decode(pkt, &d) {
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
	if err := m.client.SendPacket(&packets.C2SChunkBatchReceived{
		ChunksPerTick: ns.Float32(25.0),
	}); err != nil {
		m.client.Debugf("world: acknowledge chunk batch: %v", err)
	}
}

// GetChunks returns independent deep copies of all loaded chunk columns, taken
// under one read lock so they form a coherent snapshot.
func (m *Module) GetChunks() []*chunks.Column {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*chunks.Column, 0, len(m.chunks))
	for _, col := range m.chunks {
		if copied := m.snapshotLocked(col); copied != nil {
			result = append(result, copied)
		}
	}
	return result
}

// ChunkCacheCenter returns the current center chunk coordinates.
func (m *Module) ChunkCacheCenter() (x, z int32) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.centerChunkX, m.centerChunkZ
}

// ViewDistance returns the server-sent view distance (chunk cache radius).
func (m *Module) ViewDistance() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.viewDistance
}

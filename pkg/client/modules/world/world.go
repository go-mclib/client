package world

import (
	"sync"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/data/pkg/data/chunks"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
	"github.com/go-mclib/protocol/nbt"
)

const ModuleName = "world"

// block face constants
const (
	FaceBottom = 0 // -Y
	FaceTop    = 1 // +Y
	FaceNorth  = 2 // -Z
	FaceSouth  = 3 // +Z
	FaceWest   = 4 // -X
	FaceEast   = 5 // +X
)

// hand constants
const (
	HandMain = 0
	HandOff  = 1
)

// BlockEntityData holds the type and NBT data for a block entity.
type BlockEntityData struct {
	Type int32
	Data nbt.Compound
}

type Module struct {
	client *client.Client

	mu            sync.RWMutex
	Chunks        map[int64]*chunks.ChunkColumn
	blockEntities map[[3]int]*BlockEntityData // [x,y,z] -> data
	CenterChunkX  int32
	CenterChunkZ  int32
	ViewDistance  int32

	onChunkLoad   []func(x, z int32)
	onChunkUnload []func(x, z int32)
	onBlockUpdate []func(x, y, z int, stateID int32)
}

func New() *Module {
	return &Module{
		Chunks:        make(map[int64]*chunks.ChunkColumn),
		blockEntities: make(map[[3]int]*BlockEntityData),
		ViewDistance:  10,
	}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) { m.client = c }

func (m *Module) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Chunks = make(map[int64]*chunks.ChunkColumn)
	m.blockEntities = make(map[[3]int]*BlockEntityData)
}

// From retrieves the world module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// events

func (m *Module) OnChunkLoad(cb func(x, z int32))   { m.onChunkLoad = append(m.onChunkLoad, cb) }
func (m *Module) OnChunkUnload(cb func(x, z int32)) { m.onChunkUnload = append(m.onChunkUnload, cb) }
func (m *Module) OnBlockUpdate(cb func(x, y, z int, stateID int32)) {
	m.onBlockUpdate = append(m.onBlockUpdate, cb)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
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
	}
}

func (m *Module) handleChunkData(pkt *jp.WirePacket) {
	var d packets.S2CLevelChunkWithLight
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Printf("failed to read chunk packet: %v", err)
		return
	}

	column, err := chunks.ParseChunkColumn(int32(d.ChunkX), int32(d.ChunkZ), d.ChunkData, &d.LightData)
	if err != nil {
		m.client.Logger.Printf("failed to parse chunk column at (%d, %d): %v", d.ChunkX, d.ChunkZ, err)
		return
	}

	cx, cz := int32(d.ChunkX), int32(d.ChunkZ)
	m.mu.Lock()
	m.Chunks[chunkKey(cx, cz)] = column
	// store block entities from chunk data
	for _, be := range column.BlockEntities {
		x := int(cx)*16 + be.X()
		y := int(be.Y)
		z := int(cz)*16 + be.Z()
		if c, ok := be.Data.(nbt.Compound); ok {
			m.blockEntities[[3]int{x, y, z}] = &BlockEntityData{
				Type: int32(be.Type),
				Data: c,
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
	baseX, baseZ := int(cx)*16, int(cz)*16
	m.mu.Lock()
	delete(m.Chunks, chunkKey(cx, cz))
	for key := range m.blockEntities {
		if key[0] >= baseX && key[0] < baseX+16 && key[2] >= baseZ && key[2] < baseZ+16 {
			delete(m.blockEntities, key)
		}
	}
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

	chunkX, chunkZ := chunks.ChunkPos(int(d.Location.X), int(d.Location.Z))

	m.mu.Lock()
	chunk := m.Chunks[chunkKey(chunkX, chunkZ)]
	if chunk != nil {
		chunk.SetBlockState(int(d.Location.X), int(d.Location.Y), int(d.Location.Z), int32(d.BlockId))
	}
	m.mu.Unlock()

	for _, cb := range m.onBlockUpdate {
		cb(int(d.Location.X), int(d.Location.Y), int(d.Location.Z), int32(d.BlockId))
	}
}

func (m *Module) handleSectionBlocksUpdate(pkt *jp.WirePacket) {
	var d packets.S2CSectionBlocksUpdate
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	sectionX, sectionY, sectionZ := chunks.DecodeSectionPosition(int64(d.ChunkSectionPosition))

	m.mu.Lock()
	chunk := m.Chunks[chunkKey(sectionX, sectionZ)]
	if chunk != nil {
		sectionIndex := chunks.SectionIndex(int(sectionY) * 16)
		if sectionIndex >= 0 && sectionIndex < len(chunk.Sections) {
			section := chunk.Sections[sectionIndex]
			if section != nil {
				for _, block := range d.Blocks {
					stateID, localX, localY, localZ := chunks.DecodeBlockEntry(int64(block))
					section.SetBlockState(localX, localY, localZ, stateID)
				}
			}
		}
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
	m.mu.Lock()
	m.CenterChunkX = int32(d.ChunkX)
	m.CenterChunkZ = int32(d.ChunkZ)
	m.mu.Unlock()
}

func (m *Module) handleSetChunkCacheRadius(pkt *jp.WirePacket) {
	var d packets.S2CSetChunkCacheRadius
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	m.mu.Lock()
	m.ViewDistance = int32(d.ViewDistance)
	m.mu.Unlock()
}

func (m *Module) handleChunkBatchFinished() {
	m.client.SendPacket(&packets.C2SChunkBatchReceived{
		ChunksPerTick: ns.Float32(25.0),
	})
}

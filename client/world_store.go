package client

import (
	"sync"

	"github.com/go-mclib/data/pkg/packets"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// WorldStore stores chunk data and world state
type WorldStore struct {
	mu sync.RWMutex

	// Chunk storage: map[chunkKey]*ChunkColumn
	Chunks map[int64]*ChunkColumn

	// View distance settings
	CenterChunkX int32
	CenterChunkZ int32
	ViewDistance int32

	// Reference to client for sending packets
	client *Client
}

// NewWorldStore creates a new WorldStore
func NewWorldStore(client *Client) *WorldStore {
	return &WorldStore{
		Chunks:       make(map[int64]*ChunkColumn),
		ViewDistance: 10, // default view distance
		client:       client,
	}
}

// HandlePacket handles world-related packets
func (w *WorldStore) HandlePacket(c *Client, pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packet_ids.S2CLevelChunkWithLightID:
		w.handleChunkData(pkt)
	case packet_ids.S2CForgetLevelChunkID:
		w.handleUnloadChunk(pkt)
	case packet_ids.S2CBlockUpdateID:
		w.handleBlockUpdate(pkt)
	case packet_ids.S2CSectionBlocksUpdateID:
		w.handleSectionBlocksUpdate(pkt)
	case packet_ids.S2CSetChunkCacheCenterID:
		w.handleSetChunkCacheCenter(pkt)
	case packet_ids.S2CSetChunkCacheRadiusID:
		w.handleSetChunkCacheRadius(pkt)
	case packet_ids.S2CChunkBatchFinishedID:
		w.handleChunkBatchFinished()
	}
}

func (w *WorldStore) handleChunkData(pkt *jp.WirePacket) {
	// Parse the packet manually since the struct doesn't match the actual packet format
	// Format: ChunkX (Int), ChunkZ (Int), Heightmaps (NBT), Size (VarInt), Data, BlockEntities, LightData...
	reader := newChunkReader(pkt.Data)

	// Read chunk coordinates
	chunkX, err := reader.readInt()
	if err != nil {
		if w.client != nil {
			w.client.Logger.Printf("failed to read chunk X: %v", err)
		}
		return
	}

	chunkZ, err := reader.readInt()
	if err != nil {
		if w.client != nil {
			w.client.Logger.Printf("failed to read chunk Z: %v", err)
		}
		return
	}

	// The rest of the packet (heightmaps NBT + chunk data + block entities + light)
	// is passed to parseChunkData which will handle it
	remainingData := pkt.Data[reader.offset:]

	column, err := parseChunkData(chunkX, chunkZ, remainingData)
	if err != nil {
		if w.client != nil {
			w.client.Logger.Printf("failed to parse chunk column at (%d, %d): %v", chunkX, chunkZ, err)
		}
		return
	}

	w.mu.Lock()
	w.Chunks[chunkKey(chunkX, chunkZ)] = column
	w.mu.Unlock()
}

func (w *WorldStore) handleUnloadChunk(pkt *jp.WirePacket) {
	var d packets.S2CForgetLevelChunk
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	w.mu.Lock()
	delete(w.Chunks, chunkKey(int32(d.ChunkX), int32(d.ChunkZ)))
	w.mu.Unlock()
}

func (w *WorldStore) handleBlockUpdate(pkt *jp.WirePacket) {
	var d packets.S2CBlockUpdate
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	// Convert block position to chunk coordinates
	chunkX := int32(d.Location.X >> 4)
	chunkZ := int32(d.Location.Z >> 4)

	w.mu.Lock()
	defer w.mu.Unlock()

	chunk := w.Chunks[chunkKey(chunkX, chunkZ)]
	if chunk == nil {
		return
	}

	// Calculate section index and local coordinates
	// Y ranges from -64 to 319, section 0 starts at Y -64
	y := int(d.Location.Y)
	sectionIndex := (y + 64) / 16
	if sectionIndex < 0 || sectionIndex >= 24 {
		return
	}

	section := chunk.Sections[sectionIndex]
	if section == nil || section.BlockStates == nil {
		return
	}

	localX := int(d.Location.X) & 0xF
	localY := y & 0xF
	localZ := int(d.Location.Z) & 0xF

	section.BlockStates.SetBlockState(localX, localY, localZ, int32(d.BlockId))
}

func (w *WorldStore) handleSectionBlocksUpdate(pkt *jp.WirePacket) {
	var d packets.S2CSectionBlocksUpdate
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	// Decode chunk section position
	// X: 22 bits, Z: 22 bits, Y: 20 bits (from left to right in a 64-bit long)
	pos := int64(d.ChunkSectionPosition)
	sectionX := int32(pos >> 42)
	sectionZ := int32(pos << 22 >> 42)
	sectionY := int32(pos << 44 >> 44)

	// Sign extend the coordinates
	if sectionX >= 0x200000 {
		sectionX -= 0x400000
	}
	if sectionZ >= 0x200000 {
		sectionZ -= 0x400000
	}
	if sectionY >= 0x80000 {
		sectionY -= 0x100000
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	chunk := w.Chunks[chunkKey(sectionX, sectionZ)]
	if chunk == nil {
		return
	}

	// Convert section Y to section index (Y -4 = section 0, Y 0 = section 4, etc.)
	sectionIndex := int(sectionY) + 4
	if sectionIndex < 0 || sectionIndex >= 24 {
		return
	}

	section := chunk.Sections[sectionIndex]
	if section == nil || section.BlockStates == nil {
		return
	}

	// Parse blocks from raw byte array - each entry is a VarLong
	// Each block entry: block state ID << 12 | (x << 8 | z << 4 | y)
	reader := newChunkReader(d.Blocks) // FIXME: cannot use d.Blocks (variable of slice type net_structures.PrefixedArray[net_structures.VarLong]) as []byte value in argument to newChunkReader
	for reader.remaining() > 0 {
		block, err := reader.readVarLong()
		if err != nil {
			break
		}
		blockState := int32(block >> 12)
		localPos := int(block & 0xFFF)
		localX := (localPos >> 8) & 0xF
		localZ := (localPos >> 4) & 0xF
		localY := localPos & 0xF

		section.BlockStates.SetBlockState(localX, localY, localZ, blockState)
	}
}

func (w *WorldStore) handleSetChunkCacheCenter(pkt *jp.WirePacket) {
	var d packets.S2CSetChunkCacheCenter
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	w.mu.Lock()
	w.CenterChunkX = int32(d.ChunkX)
	w.CenterChunkZ = int32(d.ChunkZ)
	w.mu.Unlock()
}

func (w *WorldStore) handleSetChunkCacheRadius(pkt *jp.WirePacket) {
	var d packets.S2CSetChunkCacheRadius
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	w.mu.Lock()
	w.ViewDistance = int32(d.ViewDistance)
	w.mu.Unlock()
}

func (w *WorldStore) handleChunkBatchFinished() {
	// Send acknowledgment to server
	// Using a reasonable chunks per tick value (vanilla client calculates this dynamically)
	reply := &packets.C2SChunkBatchReceived{
		ChunksPerTick: ns.Float32(25.0), // reasonable default
	}

	if w.client != nil {
		w.client.OutgoingPacketQueue <- reply
	}
}

// GetBlock returns the block state ID at the given world coordinates
func (w *WorldStore) GetBlock(x, y, z int) int32 {
	chunkX := int32(x >> 4)
	chunkZ := int32(z >> 4)

	w.mu.RLock()
	chunk := w.Chunks[chunkKey(chunkX, chunkZ)]
	w.mu.RUnlock()

	if chunk == nil {
		return 0
	}

	// Calculate section index (Y -64 = section 0)
	sectionIndex := (y + 64) / 16
	if sectionIndex < 0 || sectionIndex >= 24 {
		return 0
	}

	section := chunk.Sections[sectionIndex]
	if section == nil || section.BlockStates == nil {
		return 0
	}

	localX := x & 0xF
	localY := y & 0xF
	localZ := z & 0xF

	return section.BlockStates.GetBlockState(localX, localY, localZ)
}

// setBlock sets the block state ID at the given world coordinates (client-side only)
func (w *WorldStore) setBlock(x, y, z int, blockState int32) {
	chunkX := int32(x >> 4)
	chunkZ := int32(z >> 4)

	w.mu.Lock()
	defer w.mu.Unlock()

	chunk := w.Chunks[chunkKey(chunkX, chunkZ)]
	if chunk == nil {
		return
	}

	sectionIndex := (y + 64) / 16
	if sectionIndex < 0 || sectionIndex >= 24 {
		return
	}

	section := chunk.Sections[sectionIndex]
	if section == nil {
		// Create section if it doesn't exist
		section = &ChunkSection{
			BlockStates: &PalettedContainer{
				BitsPerEntry: 0,
				SingleValue:  0, // air
			},
		}
		chunk.Sections[sectionIndex] = section
	}

	if section.BlockStates == nil {
		section.BlockStates = &PalettedContainer{
			BitsPerEntry: 0,
			SingleValue:  0,
		}
	}

	localX := x & 0xF
	localY := y & 0xF
	localZ := z & 0xF

	section.BlockStates.SetBlockState(localX, localY, localZ, blockState)
}

// IsChunkLoaded checks if a chunk is loaded at the given chunk coordinates
func (w *WorldStore) IsChunkLoaded(chunkX, chunkZ int32) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.Chunks[chunkKey(chunkX, chunkZ)]
	return ok
}

// GetChunk returns the chunk column at the given chunk coordinates
func (w *WorldStore) GetChunk(chunkX, chunkZ int32) *ChunkColumn {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.Chunks[chunkKey(chunkX, chunkZ)]
}

// GetLoadedChunkCount returns the number of loaded chunks
func (w *WorldStore) GetLoadedChunkCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.Chunks)
}

// Clear removes all loaded chunks
func (w *WorldStore) Clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Chunks = make(map[int64]*ChunkColumn)
}

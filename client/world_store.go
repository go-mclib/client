package client

import (
	"sync"

	"github.com/go-mclib/data/pkg/data/chunks"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// WorldStore stores chunk data and world state
type WorldStore struct {
	mu sync.RWMutex

	// Chunk storage: map[chunkKey]*chunks.ChunkColumn
	Chunks map[int64]*chunks.ChunkColumn

	// View distance settings
	CenterChunkX int32
	CenterChunkZ int32
	ViewDistance  int32

	// Reference to client for sending packets
	client *Client
}

// NewWorldStore creates a new WorldStore
func NewWorldStore(client *Client) *WorldStore {
	return &WorldStore{
		Chunks:       make(map[int64]*chunks.ChunkColumn),
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
	var d packets.S2CLevelChunkWithLight
	if err := pkt.ReadInto(&d); err != nil {
		if w.client != nil {
			w.client.Logger.Printf("failed to read chunk packet: %v", err)
		}
		return
	}

	column, err := chunks.ParseChunkColumn(int32(d.ChunkX), int32(d.ChunkZ), d.ChunkData, &d.LightData)
	if err != nil {
		if w.client != nil {
			w.client.Logger.Printf("failed to parse chunk column at (%d, %d): %v", d.ChunkX, d.ChunkZ, err)
		}
		return
	}

	w.mu.Lock()
	w.Chunks[chunkKey(int32(d.ChunkX), int32(d.ChunkZ))] = column
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

	chunkX, chunkZ := chunks.ChunkPos(int(d.Location.X), int(d.Location.Z))

	w.mu.Lock()
	defer w.mu.Unlock()

	chunk := w.Chunks[chunkKey(chunkX, chunkZ)]
	if chunk == nil {
		return
	}

	chunk.SetBlockState(int(d.Location.X), int(d.Location.Y), int(d.Location.Z), int32(d.BlockId))
}

func (w *WorldStore) handleSectionBlocksUpdate(pkt *jp.WirePacket) {
	var d packets.S2CSectionBlocksUpdate
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	sectionX, sectionY, sectionZ := chunks.DecodeSectionPosition(int64(d.ChunkSectionPosition))

	w.mu.Lock()
	defer w.mu.Unlock()

	chunk := w.Chunks[chunkKey(sectionX, sectionZ)]
	if chunk == nil {
		return
	}

	sectionIndex := chunks.SectionIndex(int(sectionY) * 16)
	if sectionIndex < 0 {
		return
	}

	section := chunk.Sections[sectionIndex]
	if section == nil {
		return
	}

	for _, block := range d.Blocks {
		stateID, localX, localY, localZ := chunks.DecodeBlockEntry(int64(block))
		section.SetBlockState(localX, localY, localZ, stateID)
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
	chunkX, chunkZ := chunks.ChunkPos(x, z)

	w.mu.RLock()
	chunk := w.Chunks[chunkKey(chunkX, chunkZ)]
	w.mu.RUnlock()

	if chunk == nil {
		return 0
	}

	return chunk.GetBlockState(x, y, z)
}

// setBlock sets the block state ID at the given world coordinates (client-side only)
func (w *WorldStore) setBlock(x, y, z int, blockState int32) {
	chunkX, chunkZ := chunks.ChunkPos(x, z)

	w.mu.Lock()
	defer w.mu.Unlock()

	chunk := w.Chunks[chunkKey(chunkX, chunkZ)]
	if chunk == nil {
		return
	}

	chunk.SetBlockState(x, y, z, blockState)
}

// IsChunkLoaded checks if a chunk is loaded at the given chunk coordinates
func (w *WorldStore) IsChunkLoaded(chunkX, chunkZ int32) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.Chunks[chunkKey(chunkX, chunkZ)]
	return ok
}

// GetChunk returns the chunk column at the given chunk coordinates
func (w *WorldStore) GetChunk(chunkX, chunkZ int32) *chunks.ChunkColumn {
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
	w.Chunks = make(map[int64]*chunks.ChunkColumn)
}

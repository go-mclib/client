package client

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"github.com/go-mclib/protocol/nbt"
)

// ChunkColumn represents a vertical column of 24 chunk sections (Y -64 to 319 for 1.21)
type ChunkColumn struct {
	X, Z       int32
	Sections   [24]*ChunkSection // Index 0 = Y -64, Index 23 = Y 304
	Heightmaps nbt.Compound      // Heightmap data from the chunk
}

// ChunkSection represents a 16x16x16 section of blocks
type ChunkSection struct {
	BlockCount  int16
	BlockStates *PalettedContainer
}

// PalettedContainer stores block states in a palette-indexed format
type PalettedContainer struct {
	BitsPerEntry int
	Palette      []int32  // Block state IDs (nil for direct palette)
	Data         []uint64 // Packed block indices
	SingleValue  int32    // Used when bitsPerEntry == 0
}

// GetBlockState returns the block state ID at the given position within the section
func (p *PalettedContainer) GetBlockState(x, y, z int) int32 {
	if p.BitsPerEntry == 0 {
		return p.SingleValue
	}

	index := (y*16+z)*16 + x
	blocksPerLong := 64 / p.BitsPerEntry
	longIndex := index / blocksPerLong
	bitIndex := (index % blocksPerLong) * p.BitsPerEntry

	if longIndex >= len(p.Data) {
		return 0
	}

	mask := uint64((1 << p.BitsPerEntry) - 1)
	paletteIndex := int((p.Data[longIndex] >> bitIndex) & mask)

	// Direct palette (no palette array)
	if p.Palette == nil {
		return int32(paletteIndex)
	}

	if paletteIndex >= len(p.Palette) {
		return 0
	}

	return p.Palette[paletteIndex]
}

// SetBlockState sets the block state ID at the given position within the section
func (p *PalettedContainer) SetBlockState(x, y, z int, blockState int32) {
	if p.BitsPerEntry == 0 {
		// Single value palette - need to expand if setting a different value
		if p.SingleValue == blockState {
			return
		}
		// Expand to at least 4 bits per entry
		p.expandPalette(blockState)
	}

	// Find or add to palette
	paletteIndex := -1
	if p.Palette != nil {
		for i, v := range p.Palette {
			if v == blockState {
				paletteIndex = i
				break
			}
		}
		if paletteIndex == -1 {
			// Add to palette if there's room
			maxPaletteSize := 1 << p.BitsPerEntry
			if len(p.Palette) < maxPaletteSize {
				paletteIndex = len(p.Palette)
				p.Palette = append(p.Palette, blockState)
			} else {
				// Need to resize palette
				p.expandPalette(blockState)
				paletteIndex = len(p.Palette) - 1
			}
		}
	} else {
		// Direct palette
		paletteIndex = int(blockState)
	}

	index := (y*16+z)*16 + x
	blocksPerLong := 64 / p.BitsPerEntry
	longIndex := index / blocksPerLong
	bitIndex := (index % blocksPerLong) * p.BitsPerEntry

	// Ensure data array is large enough
	for longIndex >= len(p.Data) {
		p.Data = append(p.Data, 0)
	}

	mask := uint64((1 << p.BitsPerEntry) - 1)
	p.Data[longIndex] &= ^(mask << bitIndex)
	p.Data[longIndex] |= uint64(paletteIndex) << bitIndex
}

// expandPalette expands a single-value palette to an indexed palette
func (p *PalettedContainer) expandPalette(newValue int32) {
	oldValue := p.SingleValue
	p.BitsPerEntry = 4
	p.Palette = []int32{oldValue, newValue}

	// Initialize data array with all zeros (pointing to first palette entry)
	blocksPerLong := 64 / p.BitsPerEntry
	dataLen := (4096 + blocksPerLong - 1) / blocksPerLong
	p.Data = make([]uint64, dataLen)
}

// chunkReader is a helper for reading chunk data
type chunkReader struct {
	data   []byte
	offset int
}

func newChunkReader(data []byte) *chunkReader {
	return &chunkReader{data: data, offset: 0}
}

func (r *chunkReader) remaining() int {
	return len(r.data) - r.offset
}

func (r *chunkReader) readByte() (byte, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	b := r.data[r.offset]
	r.offset++
	return b, nil
}

func (r *chunkReader) readShort() (int16, error) {
	if r.offset+2 > len(r.data) {
		return 0, io.EOF
	}
	v := int16(binary.BigEndian.Uint16(r.data[r.offset:]))
	r.offset += 2
	return v, nil
}

func (r *chunkReader) readVarInt() (int32, error) {
	var result int32
	var shift uint
	for {
		if r.offset >= len(r.data) {
			return 0, io.EOF
		}
		b := r.data[r.offset]
		r.offset++
		result |= int32(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 32 {
			return 0, errors.New("VarInt too big")
		}
	}
	return result, nil
}

func (r *chunkReader) readLong() (int64, error) {
	if r.offset+8 > len(r.data) {
		return 0, io.EOF
	}
	v := int64(binary.BigEndian.Uint64(r.data[r.offset:]))
	r.offset += 8
	return v, nil
}

func (r *chunkReader) readVarLong() (int64, error) {
	var result int64
	var shift uint
	for {
		if r.offset >= len(r.data) {
			return 0, io.EOF
		}
		b := r.data[r.offset]
		r.offset++
		result |= int64(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 64 {
			return 0, errors.New("VarLong too big")
		}
	}
	return result, nil
}

func (r *chunkReader) skip(n int) error {
	if r.offset+n > len(r.data) {
		return io.EOF
	}
	r.offset += n
	return nil
}

func (r *chunkReader) readInt() (int32, error) {
	if r.offset+4 > len(r.data) {
		return 0, io.EOF
	}
	v := int32(binary.BigEndian.Uint32(r.data[r.offset:]))
	r.offset += 4
	return v, nil
}

// readNetworkNBT reads a network NBT compound from the reader using the nbt package.
// Since Minecraft 1.20.2, network NBT omits the root compound tag type and name.
func (r *chunkReader) readNetworkNBT() (nbt.Tag, error) {
	// Create a bytes.Reader from remaining data so we can track position
	br := bytes.NewReader(r.data[r.offset:])
	nbtReader := nbt.NewReaderFrom(br)

	// Read network format NBT (nameless root)
	tag, _, err := nbtReader.ReadTag(true)
	if err != nil {
		return nil, err
	}

	// Update our offset based on how many bytes were consumed
	// bytes.Reader's position = original length - remaining bytes
	consumed := len(r.data[r.offset:]) - br.Len()
	r.offset += consumed

	return tag, nil
}

// parseChunkData parses the Data field from S2CLevelChunkWithLight into a ChunkColumn
func parseChunkData(chunkX, chunkZ int32, data []byte) (*ChunkColumn, error) {
	column := &ChunkColumn{
		X: chunkX,
		Z: chunkZ,
	}

	reader := newChunkReader(data)

	// Parse heightmaps NBT compound (network NBT format since 1.20.2)
	heightmaps, err := reader.readNetworkNBT()
	if err != nil {
		return nil, errors.New("failed to parse heightmaps NBT: " + err.Error())
	}
	if compound, ok := heightmaps.(nbt.Compound); ok {
		column.Heightmaps = compound
	}

	// Read chunk data size (VarInt)
	dataSize, err := reader.readVarInt()
	if err != nil {
		return nil, errors.New("failed to read chunk data size: " + err.Error())
	}

	// Mark the end of chunk section data
	chunkDataEnd := reader.offset + int(dataSize)

	// Parse each chunk section (24 sections for 1.21: Y -64 to 319)
	for sectionIndex := 0; sectionIndex < 24 && reader.offset < chunkDataEnd; sectionIndex++ {
		section, err := parseChunkSection(reader)
		if err != nil {
			return nil, errors.New("failed to parse chunk section: " + err.Error())
		}
		column.Sections[sectionIndex] = section
	}

	return column, nil
}

// parseChunkSection parses a single 16x16x16 chunk section
func parseChunkSection(reader *chunkReader) (*ChunkSection, error) {
	// Block count (short)
	blockCount, err := reader.readShort()
	if err != nil {
		return nil, err
	}

	section := &ChunkSection{
		BlockCount: blockCount,
	}

	// Parse block states paletted container
	blockStates, err := parsePalettedContainer(reader, 4, 8, 15)
	if err != nil {
		return nil, errors.New("failed to parse block states: " + err.Error())
	}
	section.BlockStates = blockStates

	// Parse biomes paletted container (skip for now, but need to read it)
	_, err = parsePalettedContainer(reader, 1, 3, 6)
	if err != nil {
		return nil, errors.New("failed to parse biomes: " + err.Error())
	}

	return section, nil
}

// parsePalettedContainer parses a paletted container from the chunk data
// minBits: minimum bits per entry when using indirect palette (4 for blocks, 1 for biomes)
// maxBits: maximum bits per entry before switching to direct palette (8 for blocks, 3 for biomes)
// directBits: bits per entry when using direct palette (15 for blocks, 6 for biomes)
func parsePalettedContainer(reader *chunkReader, minBits, maxBits, directBits int) (*PalettedContainer, error) {
	// Bits per entry (ubyte)
	bitsPerEntry, err := reader.readByte()
	if err != nil {
		return nil, err
	}

	container := &PalettedContainer{
		BitsPerEntry: int(bitsPerEntry),
	}

	if bitsPerEntry == 0 {
		// Single valued - palette has one entry
		value, err := reader.readVarInt()
		if err != nil {
			return nil, err
		}
		container.SingleValue = value

		// Data array length should be 0
		dataLength, err := reader.readVarInt()
		if err != nil {
			return nil, err
		}
		if dataLength != 0 {
			// Skip any data anyway
			for i := int32(0); i < dataLength; i++ {
				if _, err := reader.readLong(); err != nil {
					return nil, err
				}
			}
		}
	} else if int(bitsPerEntry) <= maxBits {
		// Indirect palette
		effectiveBits := int(bitsPerEntry)
		if effectiveBits < minBits {
			effectiveBits = minBits
		}
		container.BitsPerEntry = effectiveBits

		// Read palette
		paletteLength, err := reader.readVarInt()
		if err != nil {
			return nil, err
		}
		container.Palette = make([]int32, paletteLength)
		for i := int32(0); i < paletteLength; i++ {
			value, err := reader.readVarInt()
			if err != nil {
				return nil, err
			}
			container.Palette[i] = value
		}

		// Read data array
		dataLength, err := reader.readVarInt()
		if err != nil {
			return nil, err
		}
		container.Data = make([]uint64, dataLength)
		for i := int32(0); i < dataLength; i++ {
			value, err := reader.readLong()
			if err != nil {
				return nil, err
			}
			container.Data[i] = uint64(value)
		}
	} else {
		// Direct palette (no palette array, values stored directly)
		container.BitsPerEntry = directBits
		container.Palette = nil

		// Read data array
		dataLength, err := reader.readVarInt()
		if err != nil {
			return nil, err
		}
		container.Data = make([]uint64, dataLength)
		for i := int32(0); i < dataLength; i++ {
			value, err := reader.readLong()
			if err != nil {
				return nil, err
			}
			container.Data[i] = uint64(value)
		}
	}

	return container, nil
}

// chunkKey creates a unique key for a chunk position
func chunkKey(chunkX, chunkZ int32) int64 {
	return int64(chunkX)<<32 | int64(uint32(chunkZ))
}

package world

func chunkKey(chunkX, chunkZ int32) int64 {
	return int64(chunkX)<<32 | int64(uint32(chunkZ))
}

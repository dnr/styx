package common

func AppendBlocksList(blocks []uint16, size int64, blockShift BlkShift) []uint16 {
	nChunks := ChunkShift.Blocks(size)
	allButLast := TruncU16(ChunkShift.Size() >> blockShift)
	for j := 0; j < int(nChunks)-1; j++ {
		blocks = append(blocks, allButLast)
	}
	lastChunkLen := ChunkShift.Leftover(size)
	blocks = append(blocks, TruncU16(blockShift.Blocks(lastChunkLen)))
	return blocks
}

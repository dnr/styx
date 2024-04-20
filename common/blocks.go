package common

func MakeBlocksList(size int64, chunkShift, blockShift BlkShift) []uint16 {
	blocks := make([]uint16, chunkShift.Blocks(size))
	allButLast := TruncU16(chunkShift.Size() >> blockShift)
	for j := range blocks {
		blocks[j] = allButLast
	}
	lastChunkLen := chunkShift.Leftover(size)
	blocks[len(blocks)-1] = TruncU16(blockShift.Blocks(lastChunkLen))
	return blocks
}

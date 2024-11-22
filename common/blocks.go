package common

import "github.com/dnr/styx/common/shift"

func AppendBlocksList(blocks []uint16, size int64, blockShift, chunkShift shift.Shift) []uint16 {
	nChunks := chunkShift.Blocks(size)
	allButLast := TruncU16(chunkShift.Size() >> blockShift)
	for j := 0; j < int(nChunks)-1; j++ {
		blocks = append(blocks, allButLast)
	}
	lastChunkLen := chunkShift.Leftover(size)
	blocks = append(blocks, TruncU16(blockShift.Blocks(lastChunkLen)))
	return blocks
}

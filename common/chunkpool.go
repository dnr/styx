package common

import "sync"

type ChunkPool struct {
	p12, p14, pch *sync.Pool
}

func NewChunkPool(chunkShift int) *ChunkPool {
	return &ChunkPool{
		p12: &sync.Pool{New: func() any { return make([]byte, 1<<12) }},
		p14: &sync.Pool{New: func() any { return make([]byte, 1<<14) }},
		pch: &sync.Pool{New: func() any { return make([]byte, 1<<chunkShift) }},
	}
}

func (cp *ChunkPool) Get(size int) []byte {
	switch {
	case size <= 1<<12:
		return cp.p12.Get().([]byte)
	case size <= 1<<14:
		return cp.p14.Get().([]byte)
	default:
		return cp.pch.Get().([]byte)
	}
}

func (cp *ChunkPool) Put(b []byte) {
	size := cap(b)
	switch {
	case size <= 1<<12:
		cp.p12.Put(b)
	case size <= 1<<14:
		cp.p14.Put(b)
	default:
		cp.pch.Put(b)
	}
}

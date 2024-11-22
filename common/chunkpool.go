package common

import "sync"

type ChunkPool struct {
	// if shift.MaxChunkShift > 20, add more here:
	p12, p14, p16, p18, p20 sync.Pool
}

func NewChunkPool() *ChunkPool {
	return &ChunkPool{
		p12: sync.Pool{New: func() any { return make([]byte, 1<<12) }},
		p14: sync.Pool{New: func() any { return make([]byte, 1<<14) }},
		p16: sync.Pool{New: func() any { return make([]byte, 1<<16) }},
		p18: sync.Pool{New: func() any { return make([]byte, 1<<18) }},
		p20: sync.Pool{New: func() any { return make([]byte, 1<<20) }},
	}
}

func (cp *ChunkPool) Get(size int) []byte {
	switch {
	case size <= 1<<12:
		return cp.p12.Get().([]byte)
	case size <= 1<<14:
		return cp.p14.Get().([]byte)
	case size <= 1<<16:
		return cp.p16.Get().([]byte)
	case size <= 1<<18:
		return cp.p18.Get().([]byte)
	case size <= 1<<20:
		return cp.p20.Get().([]byte)
	default:
		return make([]byte, size)
	}
}

func (cp *ChunkPool) Put(b []byte) {
	size := cap(b)
	switch {
	case size <= 1<<12:
		cp.p12.Put(b)
	case size <= 1<<14:
		cp.p14.Put(b)
	case size <= 1<<16:
		cp.p16.Put(b)
	case size <= 1<<18:
		cp.p18.Put(b)
	case size <= 1<<20:
		cp.p20.Put(b)
	}
}

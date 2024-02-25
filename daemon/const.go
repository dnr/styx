package daemon

const (
	// tag for cachefiled. not sure if it's useful to have more than one.
	cacheTag = "styx0"

	domainId = "styx"

	dbFilename = "styx.bolt"

	slabPrefix = "_slab_"

	// TODO: eventually these can be configurable but let's fix them for now for simplicity
	fBlockShift = 12
	fBlockSize  = 1 << fBlockShift
	fChunkShift = 16
	fChunkSize  = 1 << fChunkShift
	fHashBits   = 192
	fHashBytes  = fHashBits >> 3
)

var (
	metaBucket  = []byte("meta")
	chunkBucket = []byte("chunk")
	slabBucket  = []byte("slab")
	imageBucket = []byte("image")
)

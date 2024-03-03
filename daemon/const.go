package daemon

const (
	// tag for cachefiled. not sure if it's useful to have more than one.
	cacheTag = "styx0"

	domainId = "styx"

	dbFilename = "styx.bolt"

	slabPrefix = "_slab_"
)

var (
	metaBucket  = []byte("meta")
	chunkBucket = []byte("chunk")
	slabBucket  = []byte("slab")
	imageBucket = []byte("image")
)

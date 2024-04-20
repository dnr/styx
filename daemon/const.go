package daemon

const (
	// tag for cachefilesd. not sure if it's useful to have more than one.
	cacheTag = "styx0"

	domainId = "styx"

	dbFilename = "styx.bolt"

	slabPrefix = "_slab_"

	isManifestPrefix = "M/"
)

var (
	metaBucket     = []byte("meta")
	chunkBucket    = []byte("chunk")
	slabBucket     = []byte("slab")
	imageBucket    = []byte("image")
	manifestBucket = []byte("manifest")

	metaParams = []byte("params")
)

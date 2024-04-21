package daemon

const (
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

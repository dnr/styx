package daemon

const (
	dbFilename = "styx.bolt"

	slabPrefix         = "_slab_"
	slabImagePrefix    = "_slabimg_"
	manifestSlabPrefix = "_manifests_"

	isManifestPrefix = "M/"
)

var (
	metaBucket     = []byte("meta")
	chunkBucket    = []byte("chunk")
	slabBucket     = []byte("slab")
	imageBucket    = []byte("image")
	manifestBucket = []byte("manifest")
	catalogFBucket = []byte("catalogf") // name + hash -> [sysid]
	catalogRBucket = []byte("catalogr") // hash -> name

	metaSchema = []byte("schema")
	metaParams = []byte("params")
)

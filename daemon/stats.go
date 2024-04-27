package daemon

import "sync/atomic"

type (
	daemonStats struct {
		singleReqs        atomic.Int64 // chunk request count
		singleErrs        atomic.Int64 // chunk request error count
		diffReqs          atomic.Int64 // diff request count
		diffErrs          atomic.Int64 // diff request error count
		manifestCacheReqs atomic.Int64 // total manifest cache requests
		manifestCacheHits atomic.Int64 // requests that got a hit
		manifestCacheErrs atomic.Int64 // requests that got an error
		manifestReqs      atomic.Int64 // requests for new manifest
		manifestErrs      atomic.Int64 // requests for new manifest that got an error
		slabReads         atomic.Int64 // read requests to slab
		imageReads        atomic.Int64 // read requests to image
	}
)

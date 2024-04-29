package daemon

import "sync/atomic"

type (
	daemonStats struct {
		manifestCacheReqs atomic.Int64 // total manifest cache requests
		manifestCacheHits atomic.Int64 // requests that got a hit
		manifestReqs      atomic.Int64 // requests for new manifest
		manifestErrs      atomic.Int64 // requests for new manifest that got an error
		slabReads         atomic.Int64 // read requests to slab
		slabReadErrs      atomic.Int64 // failed read requests to slab
		singleReqs        atomic.Int64 // chunk request count
		singleErrs        atomic.Int64 // chunk request error count
		diffReqs          atomic.Int64 // diff request count
		diffErrs          atomic.Int64 // diff request error count
	}

	Stats struct {
		ManifestCacheReqs int64 // total manifest cache requests
		ManifestCacheHits int64 // requests that got a hit
		ManifestReqs      int64 // requests for new manifest
		ManifestErrs      int64 // requests for new manifest that got an error
		SlabReads         int64 // read requests to slab
		SlabReadErrs      int64 // failed read requests to slab
		SingleReqs        int64 // chunk request count
		SingleErrs        int64 // chunk request error count
		DiffReqs          int64 // diff request count
		DiffErrs          int64 // diff request error count
	}
)

func (s *daemonStats) export() Stats {
	return Stats{
		ManifestCacheReqs: s.manifestCacheReqs.Load(),
		ManifestCacheHits: s.manifestCacheHits.Load(),
		ManifestReqs:      s.manifestReqs.Load(),
		ManifestErrs:      s.manifestErrs.Load(),
		SlabReads:         s.slabReads.Load(),
		SlabReadErrs:      s.slabReadErrs.Load(),
		SingleReqs:        s.singleReqs.Load(),
		SingleErrs:        s.singleErrs.Load(),
		DiffReqs:          s.diffReqs.Load(),
		DiffErrs:          s.diffErrs.Load(),
	}
}

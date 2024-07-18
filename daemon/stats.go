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
		singleBytes       atomic.Int64 // chunk bytes received (uncompressed)
		singleErrs        atomic.Int64 // chunk request error count
		batchReqs         atomic.Int64 // no-base diff request count
		batchBytes        atomic.Int64 // no-base diff bytes received (compressed)
		batchErrs         atomic.Int64 // no-base diff request error count
		diffReqs          atomic.Int64 // with-base diff request count
		diffBytes         atomic.Int64 // with-base diff bytes received (compressed)
		diffErrs          atomic.Int64 // with-base diff request error count
		recompressReqs    atomic.Int64 // reqs with recompression
	}

	Stats struct {
		ManifestCacheReqs int64 // total manifest cache requests
		ManifestCacheHits int64 // requests that got a hit
		ManifestReqs      int64 // requests for new manifest
		ManifestErrs      int64 // requests for new manifest that got an error
		SlabReads         int64 // read requests to slab
		SlabReadErrs      int64 // failed read requests to slab
		SingleReqs        int64 // chunk request count
		SingleBytes       int64 // chunk bytes received (uncompressed)
		SingleErrs        int64 // chunk request error count
		BatchReqs         int64 // no-base diff request count
		BatchBytes        int64 // no-base diff bytes received (compressed)
		BatchErrs         int64 // no-base diff request error count
		DiffReqs          int64 // with-base diff request count
		DiffBytes         int64 // with-base diff bytes received (compressed)
		DiffErrs          int64 // with-base diff request error count
		RecompressReqs    int64 // reqs with recompression
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
		SingleBytes:       s.singleBytes.Load(),
		SingleErrs:        s.singleErrs.Load(),
		BatchReqs:         s.batchReqs.Load(),
		BatchBytes:        s.batchBytes.Load(),
		BatchErrs:         s.batchErrs.Load(),
		DiffReqs:          s.diffReqs.Load(),
		DiffBytes:         s.diffBytes.Load(),
		DiffErrs:          s.diffErrs.Load(),
		RecompressReqs:    s.recompressReqs.Load(),
	}
}

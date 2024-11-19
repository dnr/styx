package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

func (s *Server) handleVaporizeReq(ctx context.Context, r *VaporizeReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}

	if abs, err := filepath.Abs(r.Path); err != nil || abs != r.Path {
		return nil, mwErr(http.StatusBadRequest, "Path is not valid absolute path")
	}

	storePath := r.Name
	if storePath == "" {
		storePath = path.Base(r.Path)
	}

	sph, sphStr, _, err := ParseSphAndName(storePath)
	if err != nil {
		return nil, err
	}
	manifestSph := makeManifestSph(sph)
	ctxForChunks := withAllocateCtx(ctx, sph, false)
	ctxForManifestChunks := withAllocateCtx(ctx, manifestSph, true)

	m := &pb.Manifest{
		Params: &pb.GlobalParams{
			DigestAlgo: common.DigestAlgo,
			DigestBits: cdig.Bits,
		},
		SmallFileCutoff: manifester.DefaultSmallFileCutoff,
		Meta: &pb.ManifestMeta{
			NarinfoUrl: "<vaporize>",
			Narinfo: &pb.NarInfo{
				StorePath: storepath.StoreDir + "/" + storePath,
				Url:       "vaporize://" + r.Path,
			},
			Generator:     "styx-" + common.Version + " (vaporize)",
			GeneratedTime: time.Now().Unix(),
		},
	}

	tryClone := true

	// The order we get from WalkDir may not match the order used by nar files, but it doesn't
	// really matter as long as the files are present in the manifest with the right names and
	// digests.
	err = filepath.WalkDir(r.Path, func(fullPath string, d fs.DirEntry, inErr error) error {
		if inErr != nil {
			return inErr
		}
		var p string
		var ok bool
		if fullPath == r.Path {
			p = "/"
		} else if p, ok = strings.CutPrefix(fullPath, r.Path); !ok || p == "" || p[0] != '/' {
			return errors.New("walked out of path")
		}
		ent := &pb.Entry{Path: p}
		switch d.Type() {
		case 0:
			ent.Type = pb.EntryType_REGULAR
			info, err := d.Info()
			if err != nil {
				return err
			}
			ent.Executable = info.Mode()&0o111 == 0o111
			ent.Size = info.Size()
			if ent.Size <= manifester.DefaultSmallFileCutoff {
				if ent.InlineData, err = os.ReadFile(fullPath); err != nil {
					return err
				}
			} else {
				digests, cshift, err := s.vaporizeFile(ctxForChunks, fullPath, ent.Size, &tryClone)
				if err != nil {
					return err
				}
				ent.Digests = cdig.ToSliceAlias(digests)
				if cshift != common.DefaultChunkShift {
					ent.ChunkShift = int32(cshift)
				}
			}

		case fs.ModeDir:
			ent.Type = pb.EntryType_DIRECTORY

		case fs.ModeSymlink:
			ent.Type = pb.EntryType_SYMLINK
			lnk, err := os.Readlink(fullPath)
			if err != nil {
				return err
			}
			ent.InlineData = []byte(lnk)
			ent.Size = int64(len(ent.InlineData))

		default:
			return fmt.Errorf("%q is not regular/directory/symlink", p)
		}

		m.Entries = append(m.Entries, ent)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// get entry for manifest
	mbcfg := manifester.ManifestBuilderConfig{}
	memcs := memChunkStore{m: make(map[cdig.CDig][]byte), blkshift: s.blockShift}
	mb, err := manifester.NewManifestBuilder(mbcfg, &memcs)
	args := manifester.BuildArgs{SmallFileCutoff: manifester.SmallManifestCutoff}
	path := common.ManifestContext + "/" + storePath
	entry, err := mb.ManifestAsEntry(ctx, &args, path, m)
	if err != nil {
		return nil, err
	}

	if len(entry.InlineData) == 0 {
		// allocate space for manifest chunks in slab
		digests := cdig.FromSliceAlias(entry.Digests)
		blocks := make([]uint16, 0, len(digests))
		cshift := common.BlkShift(entry.ChunkShiftDef())
		blocks = common.AppendBlocksList(blocks, entry.Size, s.blockShift, cshift)
		locs, err := s.AllocateBatch(ctxForManifestChunks, blocks, digests)
		if err != nil {
			return nil, err
		}

		// write manifest new chunks to slab
		toWrite := make([]int, 0, len(digests))
		_ = s.db.View(func(tx *bbolt.Tx) error {
			for i, loc := range locs {
				if !s.locPresent(tx, loc) {
					toWrite = append(toWrite, i)
				}
			}
			return nil
		})
		for _, i := range toWrite {
			err = s.gotNewChunk(locs[i], digests[i], memcs.m[digests[i]])
			if err != nil {
				return nil, err
			}
		}
	}

	// make "signed manifest" without a signature
	envelope, err := proto.Marshal(&pb.SignedMessage{
		Msg: entry,
		Params: &pb.GlobalParams{
			DigestAlgo: common.DigestAlgo,
			DigestBits: int32(cdig.Bits),
		},
	})
	if err != nil {
		return nil, err
	}

	if err = s.db.Update(func(tx *bbolt.Tx) error {
		// write manifest envelope to manifest bucket.
		// this is really only so that we can track down these chunks for GC.
		// we can't diff from this image because the chunk store may not have these chunks.
		mb := tx.Bucket(manifestBucket)
		if err := mb.Put([]byte(sphStr), envelope); err != nil {
			return err
		}

		// don't write catalog, so this can't be used as a diff source

		// write image if not present
		ib := tx.Bucket(imageBucket)
		var img pb.DbImage
		if buf := ib.Get([]byte(sphStr)); buf != nil {
			if err = proto.Unmarshal(buf, &img); err != nil {
				return err
			}
		}
		if img.MountState == pb.MountState_Unknown {
			// we have no record of this, set it up as materialized
			img.StorePath = storePath
			img.Upstream = "vaporize://" + r.Path
			img.MountState = pb.MountState_Materialized

			if buf, err := proto.Marshal(&img); err != nil {
				return err
			} else {
				ib.Put([]byte(sphStr), buf)
			}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return nil, nil
}

func (s *Server) vaporizeFile(
	ctx context.Context,
	fullPath string,
	size int64,
	tryClone *bool,
) ([]cdig.CDig, common.BlkShift, error) {
	cshift := common.PickChunkShift(size)

	buf := s.chunkPool.Get(int(cshift.Size()))
	defer s.chunkPool.Put(buf)

	f, err := os.Open(fullPath)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	// first just read and hash whole file
	var digests []cdig.CDig
	for {
		n, err := f.Read(buf)
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, 0, err
		}
		digests = append(digests, cdig.Sum(buf[:n]))
	}

	// TODO: we could deduplicate chunks within this file.
	// it's rare for a file to have repeated chunks though, so don't worry about it for now.

	blocks := make([]uint16, 0, len(digests))
	blocks = common.AppendBlocksList(blocks, size, s.blockShift, cshift)
	locs, wasAllocated, err := s.preallocateBatch(ctx, blocks, digests)
	if err != nil {
		return nil, 0, err
	}

	present := make([]bool, len(locs))
	s.db.View(func(tx *bbolt.Tx) error {
		for i, loc := range locs {
			present[i] = wasAllocated[i] && s.locPresent(tx, loc)
		}
		return nil
	})

	for i, loc := range locs {
		if present[i] {
			continue
		}

		size := cshift.FileChunkSize(size, i == len(locs)-1)
		rounded := s.blockShift.Roundup(size)
		roff := int64(i) << cshift
		// since the slab file extends beyond the end of our copy, even if it's sparse,
		// CopyFileRange can only be used to copy whole blocks.
		// also, if the loc was already allocated (but not present), then it's already linked
		// to a digest. we can't take the risk of TOCTOU, so we have to read and write.
		if !wasAllocated[i] && size == rounded && *tryClone {
			s.stateLock.Lock()
			cfd := s.readfdBySlab[loc.SlabId].cacheFd
			s.stateLock.Unlock()
			if cfd == 0 {
				return nil, 0, errCachefdNotFound
			}
			woff := int64(loc.Addr) << s.blockShift
			rsize, err := unix.CopyFileRange(int(f.Fd()), &roff, cfd, &woff, int(size), 0)
			if err == nil && rsize != int(size) {
				err = io.ErrShortWrite
			}
			switch err {
			case nil:
				continue
			case syscall.EINVAL, syscall.EOPNOTSUPP, syscall.EXDEV, io.ErrShortWrite:
				log.Println("error from CopyFileRange, falling back to plain copy:", err)
				*tryClone = false
				// fall back to plain copy
			default:
				return nil, 0, err
			}
		}

		// plain copy. note we write through the writefd instead for consistency.
		b := buf[:size]
		if _, err := f.ReadAt(b, roff); err != nil {
			return nil, 0, err
		}
		err := s.gotNewChunk(loc, digests[i], b)
		if err != nil {
			return nil, 0, err
		}
	}

	// before we commit, verify all chunks that we wrote:
	for i, loc := range locs {
		if present[i] {
			continue // don't bother if it was there before we started
		}

		size := cshift.FileChunkSize(size, i == len(locs)-1)
		b := buf[:size]
		err := s.getKnownChunk(loc, b)
		if err != nil {
			return nil, 0, err
		}
		got := cdig.Sum(b)
		if got != digests[i] {
			err := fmt.Errorf("digest mismatch after vaporize: %x != %x at %d/%d", got, digests[i], loc.SlabId, loc.Addr)
			log.Print(err.Error())
			return nil, 0, err
		}
	}

	err = s.commitPreallocated(ctx, blocks, digests, locs, wasAllocated)
	if err != nil {
		return nil, 0, err
	}
	return digests, cshift, nil
}

// two-phase allocate to support vaporize
// first reserve space but don't assocate with chunks
func (s *Server) preallocateBatch(ctx context.Context, blocks []uint16, digests []cdig.CDig) ([]erofs.SlabLoc, []bool, error) {
	_, forManifest, ok := fromAllocateCtx(ctx)
	if !ok {
		return nil, nil, errors.New("missing allocate context")
	}

	n := len(blocks)
	if n != len(digests) {
		return nil, nil, errors.New("mismatched lengths")
	}
	out := make([]erofs.SlabLoc, n)
	wasAllocated := make([]bool, n)
	err := s.db.Update(func(tx *bbolt.Tx) error {
		cb, slabroot := tx.Bucket(chunkBucket), tx.Bucket(slabBucket)
		var slabId uint16 = 0
		if forManifest {
			slabId = manifestSlabOffset
		}
		sb, err := slabroot.CreateBucketIfNotExists(slabKey(slabId))
		if err != nil {
			return err
		}
		// reserve some blocks for future purposes
		seq := max(sb.Sequence(), reservedBlocks)

		for i := range out {
			digest := digests[i][:]
			if loc := cb.Get(digest); loc == nil {
				// allocate
				if seq >= slabBytes>>s.blockShift {
					slabId++
					if sb, err = slabroot.CreateBucketIfNotExists(slabKey(slabId)); err != nil {
						return err
					}
					seq = max(sb.Sequence(), reservedBlocks)
				}
				addr := common.TruncU32(seq)
				seq += uint64(blocks[i])
				out[i] = erofs.SlabLoc{slabId, addr}
			} else {
				out[i] = loadLoc(loc)
				wasAllocated[i] = true
			}
		}

		return sb.SetSequence(seq)
	})
	if err != nil {
		return nil, nil, err
	}
	return out, wasAllocated, nil
}

// next (after caller has written/cloned), associate with chunks
func (s *Server) commitPreallocated(ctx context.Context, blocks []uint16, digests []cdig.CDig, locs []erofs.SlabLoc, wasAllocated []bool) error {
	sph, forManifest, ok := fromAllocateCtx(ctx)
	if !ok {
		return errors.New("missing allocate context")
	}

	if len(blocks) != len(digests) || len(blocks) != len(locs) || len(blocks) != len(wasAllocated) {
		return errors.New("mismatched lengths")
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		cb, slabroot := tx.Bucket(chunkBucket), tx.Bucket(slabBucket)
		var slabId uint16 = 0
		if forManifest {
			slabId = manifestSlabOffset
		}
		sb, err := slabroot.CreateBucketIfNotExists(slabKey(slabId))
		if err != nil {
			return err
		}

		for i, loc := range locs {
			digest := digests[i][:]
			wasAlloc := wasAllocated[i]
			locRec := cb.Get(digest)
			isAlloc := locRec != nil

			if !wasAlloc {
				// we reserved space for a new digest in a slab before, and the caller did
				// clone or write into the space.
				if !isAlloc {
					// there's still no link from the digest to this space. create it, and mark
					// it present also.
					if err := cb.Put(digest, locValue(loc.SlabId, loc.Addr, sph)); err != nil {
						return err
					} else if err = sb.Put(addrKey(loc.Addr), digest); err != nil {
						return err
					} else if err = sb.Put(addrKey(loc.Addr|presentMask), []byte{}); err != nil {
						return err
					}
				} else {
					// we reserved space for a new digest before, but now it's associated
					// somewhere else. just forget about it. this could happen if a file
					// contains a repeated chunk.
					log.Printf("dropping vaporized data for new chunk %s at %d/%d", digests[i], loc.SlabId, loc.Addr)
				}
			} else {
				// we had data for a digest that was already mapped.
				if isAlloc {
					// it's still mapped. we can now link it to the new sph and mark it present.
					if newLoc := appendSph(locRec, sph); newLoc != nil {
						if err := cb.Put(digest, newLoc); err != nil {
							return err
						}
					}
					// actually we don't need to do this since we called gotNewChunk that updates presence:
					// if err = sb.Put(addrKey(loc.Addr|presentMask), []byte{}); err != nil {
					// 	return err
					// }
				} else {
					// it's not mapped anymore, it got gc'd?
					log.Printf("dropping vaporized data for old chunk %s at %d/%d", digests[i], loc.SlabId, loc.Addr)
				}
			}
		}
		return nil
	})
}

type memChunkStore struct {
	m        map[cdig.CDig][]byte
	blkshift common.BlkShift
}

func (m *memChunkStore) PutIfNotExists(ctx context.Context, path string, key string, data []byte) ([]byte, error) {
	if path == manifester.ChunkReadPath {
		dig, err := cdig.FromBase64(key)
		if err != nil {
			return nil, err
		}
		// data is from a chunk pool, we shouldn't hold on to it. make a copy. but leave extra
		// room so we don't have to copy again when we write.
		d := make([]byte, len(data), m.blkshift.Roundup(int64(len(data))))
		copy(d, data)
		m.m[dig] = d
	}
	return nil, nil
}

func (m *memChunkStore) Get(ctx context.Context, path string, key string, dst []byte) ([]byte, error) {
	panic("not implemented")
}

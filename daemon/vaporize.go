package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
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
	if !reStorePath.MatchString(storePath) {
		return nil, mwErr(http.StatusBadRequest, "invalid store path or missing name")
	}

	sph, _, err := ParseSph(storePath)
	if err != nil {
		return nil, err
	}
	manifestSph := makeManifestSph(sph)
	ctxForChunks := context.WithValue(ctx, "sph", sph)
	ctxForManifestChunks := context.WithValue(ctx, "sph", manifestSph)

	m := &pb.Manifest{
		Params: &pb.GlobalParams{
			ChunkShift: int32(common.ChunkShift),
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
	s.stateLock.Lock()
	readFds := maps.Clone(s.readfdBySlab)
	s.stateLock.Unlock()

	buf := s.chunkPool.Get(int(common.ChunkShift.Size()))
	defer s.chunkPool.Put(buf)

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
				f, err := os.Open(fullPath)
				if err != nil {
					return err
				}

				var digests []cdig.CDig
				for {
					n, err := f.Read(buf)
					if err == io.EOF {
						break
					} else if err != nil {
						return err
					}
					digests = append(digests, cdig.Sum(buf[:n]))
				}

				blocks := make([]uint16, 0, len(digests))
				blocks = common.AppendBlocksList(blocks, ent.Size, s.blockShift)
				// FIXME: allocate without mapping, then assign to digests after verifying, to
				// protect against toctou
				locs, err := s.AllocateBatch(ctxForChunks, blocks, digests, false)
				if err != nil {
					return err
				}

				present := make([]bool, len(locs))
				s.db.View(func(tx *bbolt.Tx) error {
					for i, loc := range locs {
						present[i] = s.locPresent(tx, loc)
					}
					return nil
				})

				for i, loc := range locs {
					if present[i] {
						continue
					}
					cfd := readFds[loc.SlabId].cacheFd
					if cfd == 0 {
						return errCachefdNotFound
					}

					size := common.ChunkShift.FileChunkSize(ent.Size, i == len(locs)-1)
					rounded := s.blockShift.Roundup(size)
					roff := int64(i) << common.ChunkShift
					woff := int64(loc.Addr) << s.blockShift
					// since the slab file extends beyond the end of our copy, even if it's
					// sparse, CopyFileRange can only be used to copy whole blocks.
					if tryClone && size == rounded {
						var rsize int
						rsize, err = unix.CopyFileRange(int(f.Fd()), &roff, cfd, &woff, int(size), 0)
						if err == nil && rsize != int(size) {
							err = io.ErrShortWrite
						}
						switch err {
						case nil:
							// nothing
						case syscall.EINVAL, syscall.EOPNOTSUPP, syscall.EXDEV, io.ErrShortWrite:
							log.Println("error from CopyFileRange, falling back to plain copy:", err)
							tryClone = false
						default:
							return err
						}
					}
					if !tryClone || size != rounded {
						// plain copy
						b := buf[:size]
						if _, err := f.ReadAt(b, roff); err != nil {
							return err
						}
						// round up to block size
						for len(b) < int(rounded) {
							b = append(b, 0)
						}
						if _, err = unix.Pwrite(cfd, b, woff); err != nil {
							return err
						}
					}
				}

				f.Close()
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

	// allocate space for manifest chunks in slab
	if len(entry.InlineData) == 0 {
		digests := cdig.FromSliceAlias(entry.Digests)
		blocks := make([]uint16, 0, len(digests))
		blocks = common.AppendBlocksList(blocks, entry.Size, s.blockShift)
		_, err := s.AllocateBatch(ctxForManifestChunks, blocks, digests, true)
		if err != nil {
			return nil, err
		}
	}

	// FIXME: write manifest chunks to slab

	// FIXME: write entry to manifest bucket

	// FIXME: write to forwards/backwards catalog

	return nil, nil
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

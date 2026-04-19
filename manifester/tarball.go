package manifester

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/resolve"
	"github.com/dnr/styx/pb"
	"github.com/multiformats/go-multihash"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/storepath"
	"google.golang.org/protobuf/proto"
)

type tarEntry struct {
	nar.Header
	offset int64
}

func (b *ManifestBuilder) BuildFromTarball(
	ctx context.Context,
	upstream string,
	shardTotal, shardIndex int,
	useLocalStoreDump string,
	writeBuildRoot bool,
) (*ManifestBuildRes, error) {
	log.Println("manifest tarball", upstream)

	// resolve the url to a hopefully-immutable url and get an etag for constructing a cache key
	rr, err := resolve.ResolveUrl(ctx, upstream)
	if err != nil {
		return nil, err
	}

	var narOut io.Reader
	var dump *exec.Cmd
	var tmpData *os.File
	if useLocalStoreDump != "" {
		// get the actual data from the local fs
		dump = exec.CommandContext(ctx, common.NixBin+"-store", "--dump", useLocalStoreDump)
		if narOut, err = dump.StdoutPipe(); err != nil {
			return nil, err
		}
		if err = dump.Start(); err != nil {
			return nil, err
		}
		defer func() {
			if dump != nil {
				dump.Process.Kill()
				dump.Wait()
			}
		}()
	} else {
		// download body
		var tarOut io.Reader
		var decompress *exec.Cmd

		res, err := common.RetryHttpRequest(ctx, http.MethodGet, rr.Url, "", nil)
		if err != nil {
			return nil, fmt.Errorf("%w: tar http error for %s: %w", ErrReq, upstream, err)
		}
		defer res.Body.Close()
		tarOut = res.Body

		// log.Println("req", storePathHash, "downloading nar")

		switch {
		case strings.HasSuffix(rr.Url, ".gz") || strings.HasSuffix(rr.Url, ".tgz"):
			decompress = exec.Command(common.GzipBin, "-d")
		case strings.HasSuffix(rr.Url, ".xz") || strings.HasSuffix(rr.Url, ".txz"):
			decompress = exec.Command(common.XzBin, "-d")
		//case strings.HasSuffix(resolved, ".zst") || strings.HasSuffix(resolved, ".zstd"):
		// TODO: use in-memory pipe?
		// 	decompress = exec.Command(common.ZstdBin, "-d")
		default:
			decompress = nil
		}
		if decompress != nil {
			decompress.Stdin = tarOut
			tarOut, err = decompress.StdoutPipe()
			if err != nil {
				return nil, fmt.Errorf("%w: can't create stdout pipe: %w", ErrInternal, err)
			}
			decompress.Stderr = os.Stderr
			if err = decompress.Start(); err != nil {
				return nil, fmt.Errorf("%w: nar decompress start error: %w", ErrInternal, err)
			}
			defer func() {
				if decompress != nil {
					decompress.Process.Kill()
					decompress.Wait()
				}
			}()
		}

		// extract tar into temporary file
		tmpData, err = os.CreateTemp("", "styx-tarball-*")
		if err != nil {
			return nil, fmt.Errorf("%w: can't create temp file: %w", ErrInternal, err)
		}
		defer os.Remove(tmpData.Name())
		defer tmpData.Close()

		tmpBuf := make([]byte, 64<<10)
		tarEnts, err := b.extractTar(tarOut, tmpData, tmpBuf)
		if err != nil {
			return nil, fmt.Errorf("%w: tar read error: %w", ErrInternal, err)
		}

		// ensure we got the whole thing
		if decompress != nil {
			if err = decompress.Wait(); err != nil {
				return nil, fmt.Errorf("%w: nar decompress error: %w", ErrInternal, err)
			}
			decompress = nil
		}

		// construct nar from contents, write to hasher and builder
		pr, pw := io.Pipe()
		go b.writeNar(tarEnts, tmpData, tmpBuf, pw)
		narOut = pr
	}

	// set up to hash nar
	narHasher, _ := hash.New(multihash.SHA2_256)

	// TODO: make args configurable again (hashed in manifest cache key)
	args := &BuildArgs{
		SmallFileCutoff: DefaultSmallFileCutoff,
		ShardTotal:      shardTotal,
		ShardIndex:      shardIndex,
	}
	manifest, err := b.buildFromNar(ctx, args, io.TeeReader(narOut, narHasher))
	if err != nil {
		return nil, fmt.Errorf("%w: manifest generation error: %w", ErrInternal, err)
	}

	if dump != nil {
		if err = dump.Wait(); err != nil {
			return nil, fmt.Errorf("%w: nar dump error: %w", ErrInternal, err)
		}
		dump = nil
	}

	// turn tar hash into store path hash using nix's fod algorithm
	innerHash := hex.EncodeToString(narHasher.Digest())
	fpHasher := sha256.New()
	// "source" is specific to nar hashing method. we don't support flat here yet.
	fmt.Fprintf(fpHasher, "source:sha256:%s:%s:%s", innerHash, storepath.StoreDir, rr.StorePathName)
	cmpHash := hash.CompressHash(fpHasher.Sum(nil), storepath.PathHashSize)
	sph := nixbase32.EncodeToString(cmpHash)

	log.Println("manifest tarball", upstream, "->", rr.Url, "built manifest", sph)

	b.stats.Shards.Add(1)

	// if we're not shard 0, we're done
	if shardIndex != 0 {
		return nil, nil
	}

	// add metadata

	nipb := &pb.NarInfo{
		StorePath:   storepath.StoreDir + "/" + sph + "-" + rr.StorePathName,
		Url:         "nar/dummy.nar",
		Compression: "none",
		FileHash:    narHasher.NixString(),
		FileSize:    int64(narHasher.BytesWritten()),
		NarHash:     narHasher.NixString(),
		NarSize:     int64(narHasher.BytesWritten()),
	}
	manifest.Meta = &pb.ManifestMeta{
		GenericTarballOriginal: upstream,
		GenericTarballResolved: rr.Url,
		Narinfo:                nipb,
		Generator:              "styx-" + common.Version,
		GeneratedTime:          time.Now().Unix(),
	}

	// turn into entry (maybe chunk)

	manifestArgs := BuildArgs{SmallFileCutoff: SmallManifestCutoff}
	entPath := common.ManifestContext + "/" + path.Base(nipb.StorePath)
	entry, err := b.ManifestAsEntry(ctx, &manifestArgs, entPath, manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: make manifest entry error: %w", ErrInternal, err)
	}

	sb, err := common.SignMessageAsEntry(b.signKeys, b.params, entry)
	if err != nil {
		return nil, fmt.Errorf("%w: sign error: %w", ErrInternal, err)
	}

	// write to cache (it'd be nice to return and do this in the background, but that doesn't
	// work on lambda)
	// TODO: we shouldn't write to cache unless we know for sure that other shards are done.
	// (or else change client to re-request manifest on missing)
	cacheKey := (&ManifestReq{
		Upstream:      rr.Url,
		StorePathHash: sph,
		DigestAlgo:    cdig.Algo,
		DigestBits:    int(cdig.Bits),
	}).CacheKey()
	cmpSb, err := b.cs.PutIfNotExists(ctx, ManifestCachePath, cacheKey, sb)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest cache write error: %w", ErrInternal, err)
	}

	if cmpSb == nil {
		// already exists in cache, need to compress ourselves
		zp := common.GetZstdCtxPool()
		z := zp.Get()
		defer zp.Put(z)
		cmpSb, err = z.Compress(nil, sb)
		if err != nil {
			return nil, fmt.Errorf("%w: manifest compress error: %w", ErrInternal, err)
		}
	}

	// write etag cache entry
	var etagCacheKey string
	if rr.Etag != "" {
		etagCacheKey = (&ManifestReq{
			Upstream:   rr.Url,
			BuildMode:  ModeGenericTarball,
			ETag:       rr.Etag,
			DigestAlgo: cdig.Algo,
			DigestBits: int(cdig.Bits),
		}).CacheKey()
		log.Println("writing etag manifest cache as", etagCacheKey)
		_, err := b.cs.PutIfNotExists(ctx, ManifestCachePath, etagCacheKey, sb)
		if err != nil {
			return nil, fmt.Errorf("%w: etag manifest cache write error: %w", ErrInternal, err)
		}
	}

	if writeBuildRoot {
		btime := time.Now()
		broot := &pb.BuildRoot{
			Meta: &pb.BuildRootMeta{
				BuildTime:        btime.Unix(),
				ManifestUpstream: rr.Url,
				ManifestSph:      ModeGenericTarball,
			},
			Manifest: []string{cacheKey},
		}
		if etagCacheKey != "" {
			broot.Manifest = append(broot.Manifest, etagCacheKey)
		}
		if brdata, err := proto.Marshal(broot); err == nil {
			brkey := strings.Join([]string{"manifest", btime.Format(time.RFC3339), "m", "m"}, "@")
			if _, err = b.cs.PutIfNotExists(ctx, BuildRootPath, brkey, brdata); err != nil {
				return nil, fmt.Errorf("%w: build root write error: %w", ErrInternal, err)
			}
		}
	}

	log.Println("manifest tarball", rr.Url, "added to cache as", cacheKey)
	b.stats.Manifests.Add(1)

	return &ManifestBuildRes{
		CacheKey:     cacheKey,
		EtagCacheKey: etagCacheKey,
		Sph:          sph,
		Bytes:        cmpSb,
	}, nil
}

func (b *ManifestBuilder) extractTar(r io.Reader, tmpData *os.File, tmpBuf []byte) ([]*tarEntry, error) {
	tr := tar.NewReader(r)

	// ensure root exists
	ents := []*tarEntry{{
		Header: nar.Header{
			Path: "/",
			Type: nar.TypeDirectory,
		},
	}}
	seen := map[string]int{"/": 0} // path -> index in ents

	for {
		ent, err := b.tarEntry(tr, tmpData, tmpBuf)
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if ent == nil {
			continue
		}

		// add missing parents
		var parents []string
		for pdir := path.Dir(ent.Path); pdir != "/" && pdir != "." && seen[pdir] == 0; pdir = path.Dir(pdir) {
			parents = append(parents, pdir)
		}
		slices.Reverse(parents)
		for _, p := range parents {
			seen[p] = len(ents)
			ents = append(ents, &tarEntry{
				Header: nar.Header{
					Path: p,
					Type: nar.TypeDirectory,
				},
			})
		}

		if idx, ok := seen[ent.Path]; ok {
			ents[idx] = ent // already seen or created as missing parent
		} else {
			seen[ent.Path] = len(ents)
			ents = append(ents, ent)
		}
	}

	// sort in nar order
	sort.Slice(ents, func(i, j int) bool {
		return narPathLess(ents[i].Path, ents[j].Path)
	})

	// do what fetchzip stripRoot does
	ents = stripRoot(ents)

	return ents, nil
}

func narPathLess(a, b string) bool {
	// nar order is element-wise lexicographical. note that all paths start with "/".
	for {
		i := strings.IndexByte(a[1:], '/')
		j := strings.IndexByte(b[1:], '/')
		if i == -1 && j == -1 {
			return a < b
		}
		if i == -1 { // a has no more components, b has more
			ac := a
			bc := b[:j+1]
			if ac != bc {
				return ac < bc
			}
			return true // a is shorter (it's the parent of b)
		}
		if j == -1 { // b has no more components, a has more
			ac := a[:i+1]
			bc := b
			if ac != bc {
				return ac < bc
			}
			return false // b is shorter
		}
		ac := a[:i+1]
		bc := b[:j+1]
		if ac != bc {
			return ac < bc
		}
		a = a[i+1:]
		b = b[j+1:]
	}
}

func (b *ManifestBuilder) tarEntry(tr *tar.Reader, tmpData *os.File, tmpBuf []byte) (*tarEntry, error) {
	h, err := tr.Next()
	if err != nil { // including io.EOF
		return nil, err
	} else if h.Typeflag == tar.TypeXGlobalHeader {
		return nil, nil // skip PAX global headers
	}

	name := path.Clean(h.Name)
	if name == "." {
		name = "/"
	} else {
		name = "/" + strings.Trim(name, "/")
	}

	e := &tarEntry{
		Header: nar.Header{
			Path:       name,
			Executable: h.Typeflag == tar.TypeReg && h.Mode&0o111 != 0,
			Size:       h.Size,
		},
	}

	switch h.Typeflag {
	case tar.TypeDir:
		e.Type = nar.TypeDirectory

	case tar.TypeReg:
		e.Type = nar.TypeRegular
		offset, err := tmpData.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, err
		}
		e.offset = offset
		n, err := io.CopyBuffer(tmpData, tr, tmpBuf)
		if err != nil {
			return nil, err
		}
		if e.Size != n {
			return nil, fmt.Errorf("tar regular file size mismatch %q %d != %d",
				h.Name, e.Size, n)
		}

	case tar.TypeSymlink:
		e.Type = nar.TypeSymlink
		e.Size = 0
		e.LinkTarget = h.Linkname

	// TODO: hard link?

	default:
		return nil, fmt.Errorf("unknown type %v", h.Typeflag)
	}

	return e, nil
}

func (b *ManifestBuilder) writeNar(ents []*tarEntry, tmpData *os.File, tmpBuf []byte, w *io.PipeWriter) (retErr error) {
	defer func() { w.CloseWithError(retErr) }()

	nw, err := nar.NewWriter(w)
	if err != nil {
		return err
	}

	for _, e := range ents {
		err = nw.WriteHeader(&e.Header)
		if err != nil {
			return err
		}
		if e.Type == nar.TypeRegular {
			_, err = io.CopyBuffer(nw, io.NewSectionReader(tmpData, e.offset, e.Size), tmpBuf)
			if err != nil {
				return err
			}
		}
	}

	return nw.Close()
}

func stripRoot(ents []*tarEntry) []*tarEntry {
	if len(ents) < 2 || ents[0].Path != "/" ||
		ents[0].Type != nar.TypeDirectory ||
		ents[1].Type != nar.TypeDirectory {
		return ents
	}
	first := ents[1]
	prefix := first.Path + "/"

	for _, e := range ents[2:] {
		if !strings.HasPrefix(e.Path, prefix) {
			return ents
		}
	}

	ents = ents[1:] // remove old root
	for _, e := range ents[1:] {
		e.Path = e.Path[len(first.Path):]
	}
	first.Path = "/"

	return ents
}

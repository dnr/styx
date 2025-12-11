package manifester

import (
	"archive/tar"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/pb"
	"github.com/multiformats/go-multihash"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/storepath"
	"google.golang.org/protobuf/proto"
)

const (
	sphGenericTarball = "@generic-tarball"
)

var reNixExprs = regexp.MustCompile(`https://releases\.nixos\.org/.*/(nixos-\d\d\.\d\d\.\d+).[a-z0-9]+/nixexprs\.tar\.xz`)

func (b *ManifestBuilder) BuildGenericTarball(
	ctx context.Context,
	upstream string,
	shardTotal, shardIndex int,
	writeBuildRoot bool,
) (*ManifestBuildRes, error) {
	log.Println("manifest generic", upstream)

	// download body
	var tarOut io.Reader
	var decompress *exec.Cmd
	// TODO: do the equivalent of useLocalStoreDump here since we have it locally in CI

	// start := time.Now()
	res, err := common.RetryHttpRequest(ctx, http.MethodGet, upstream, "", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: tar http error for %s: %w", ErrReq, upstream, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: tar http status for %s: %s", ErrReq, upstream, res.Status)
	}
	tarOut = res.Body

	// log.Println("req", storePathHash, "downloading nar")

	switch {
	case strings.HasSuffix(upstream, ".gz") || strings.HasSuffix(upstream, ".tgz"):
		decompress = exec.Command(common.GzipBin, "-d")
	case strings.HasSuffix(upstream, ".xz") || strings.HasSuffix(upstream, ".txz"):
		decompress = exec.Command(common.XzBin, "-d")
	//case strings.HasSuffix(upstream, ".zst") || strings.HasSuffix(upstream, ".zstd"):
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

	// set up to hash
	tarHasher, _ := hash.New(multihash.SHA2_256)

	// write some context to avoid collisions with anything else
	tarHasher.Write([]byte("styx/" + sphGenericTarball + "/v1" + "\n"))

	// TODO: make args configurable again (hashed in manifest cache key)
	args := &BuildArgs{
		SmallFileCutoff: DefaultSmallFileCutoff,
		ShardTotal:      shardTotal,
		ShardIndex:      shardIndex,
	}
	manifest, err := b.buildFromTar(ctx, args, io.TeeReader(tarOut, tarHasher))
	if err != nil {
		return nil, fmt.Errorf("%w: manifest generation error: %w", ErrInternal, err)
	}

	// TODO: allow passing in expected hash somehow
	// if narHasher.SRIString() != ni.NarHash.SRIString() {
	// 	return nil, fmt.Errorf("%w: nar hash mismatch", ErrReq)
	// }

	// turn tar hash into store path hash
	// TODO: it'd be really cool if we did this with the same algorithm that nix does for FODs,
	// but it doesn't matter for now.
	cmpHash := hash.CompressHash(tarHasher.Digest(), storepath.PathHashSize)
	sph := nixbase32.EncodeToString(cmpHash)

	// hack: tweak name, e.g. we want
	//   https://releases.nixos.org/nixos/25.11/nixos-25.11.1056.d9bc5c7dceb3/nixexprs.tar.xz
	// to turn into "nixexprs-nixos-25.11.1056" for better diffing
	spName := path.Base(upstream)
	if m := reNixExprs.FindStringSubmatch(upstream); m != nil {
		spName = "nixexprs-" + m[1]
	}

	if decompress != nil {
		if err = decompress.Wait(); err != nil {
			return nil, fmt.Errorf("%w: nar decompress error: %w", ErrInternal, err)
		}
		decompress = nil
		// elapsed := time.Since(start)
		// ps := decompress.ProcessState
		// log.Printf("downloaded %s [%d bytes] in %s [decmp %s user, %s sys]: %.3f MB/s",
		// 	ni.URL, size, elapsed, ps.UserTime(), ps.SystemTime(),
		// 	float64(size)/elapsed.Seconds()/1e6)
	}

	// if dump != nil {
	// 	if err = dump.Wait(); err != nil {
	// 		return nil, fmt.Errorf("%w: nar dump error: %w", ErrInternal, err)
	// 	}
	// 	dump = nil
	// }
	log.Println("manifest generic", upstream, "built manifest", sph)

	b.stats.Shards.Add(1)

	// if we're not shard 0, we're done
	if shardIndex != 0 {
		return nil, nil
	}

	// add metadata

	nipb := &pb.NarInfo{
		StorePath: "/nix/store/" + sph + "-" + spName,
		Url:       upstream,
		// Compression: ni.Compression,
		// FileHash:    ni.FileHash.NixString(),
		// FileSize:    int64(ni.FileSize),
		// NarHash:     ni.NarHash.NixString(),
		// NarSize:     int64(ni.NarSize),
		// References:  ni.References,
		// Deriver:     ni.Deriver,
		// System:      ni.System,
		// Signatures:  make([]string, len(ni.Signatures)),
		// Ca:          ni.CA,
	}
	// for i, sig := range ni.Signatures {
	// 	nipb.Signatures[i] = sig.String()
	// }
	manifest.Meta = &pb.ManifestMeta{
		NarinfoUrl:    sphGenericTarball + "/" + upstream,
		Narinfo:       nipb,
		Generator:     "styx-" + common.Version,
		GeneratedTime: time.Now().Unix(),
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
	normalizedUpstream := upstream
	common.NormalizeUpstream(&normalizedUpstream)
	cacheKey := (&ManifestReq{
		Upstream:      normalizedUpstream,
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

	if writeBuildRoot {
		btime := time.Now()
		broot := &pb.BuildRoot{
			Meta: &pb.BuildRootMeta{
				BuildTime:        btime.Unix(),
				ManifestUpstream: upstream,
				ManifestSph:      sphGenericTarball,
			},
			Manifest: []string{cacheKey},
		}
		if brdata, err := proto.Marshal(broot); err == nil {
			brkey := strings.Join([]string{"manifest", btime.Format(time.RFC3339), "m", "m"}, "@")
			if _, err = b.cs.PutIfNotExists(ctx, BuildRootPath, brkey, brdata); err != nil {
				return nil, fmt.Errorf("%w: build root write error: %w", ErrInternal, err)
			}
		}
	}

	log.Println("manifest generic", upstream, "added to cache as", cacheKey)
	b.stats.Manifests.Add(1)

	return &ManifestBuildRes{
		CacheKey: cacheKey,
		Bytes:    cmpSb,
	}, nil
}

func (b *ManifestBuilder) buildFromTar(ctx context.Context, args *BuildArgs, r io.Reader) (*pb.Manifest, error) {
	m := &pb.Manifest{
		Params:          b.params,
		SmallFileCutoff: int32(args.SmallFileCutoff),
	}

	tr := tar.NewReader(r)

	egCtx := errgroup.WithContext(ctx)
	var err error
	for err == nil && egCtx.Err() == nil {
		err = b.tarEntry(egCtx, args, m, tr)
	}
	if err == io.EOF {
		err = nil
	}

	// TODO: sort manifest here for consistency?

	return common.ValOrErr(m, cmp.Or(err, egCtx.Wait()))
}

func (b *ManifestBuilder) tarEntry(egCtx *errgroup.Group, args *BuildArgs, m *pb.Manifest, tr *tar.Reader) error {
	h, err := tr.Next()
	if err != nil { // including io.EOF
		return err
	}

	name := path.Clean(h.Name)
	if name == "." {
		name = "/"
	} else {
		name = "/" + strings.Trim(name, "/")
	}

	if name != "/" && len(m.Entries) == 0 {
		// add fake root entry
		m.Entries = append(m.Entries, &pb.Entry{
			Path: "/",
			Type: pb.EntryType_DIRECTORY,
		})
	}

	e := &pb.Entry{
		Path:       name,
		Executable: h.Typeflag == tar.TypeReg && h.Mode&0o111 != 0,
		Size:       h.Size,
	}
	m.Entries = append(m.Entries, e)

	switch h.Typeflag {
	case tar.TypeDir:
		e.Type = pb.EntryType_DIRECTORY

	case tar.TypeReg:
		e.Type = pb.EntryType_REGULAR

		var dataR io.Reader = tr

		if e.Size <= int64(args.SmallFileCutoff) {
			e.InlineData = make([]byte, e.Size)
			if _, err := io.ReadFull(dataR, e.InlineData); err != nil {
				return err
			}
		} else {
			cshift := b.chunkSizer(e.Size)
			if cshift != shift.DefaultChunkShift {
				e.ChunkShift = int32(cshift)
			}
			var err error
			e.Digests, err = b.chunkData(egCtx, args, e.Size, cshift, dataR)
			if err != nil {
				return err
			}
		}

	case tar.TypeSymlink:
		e.Type = pb.EntryType_SYMLINK
		e.InlineData = []byte(h.Linkname)

	// TODO: hard link?

	default:
		return errors.New("unknown type")
	}

	return nil
}

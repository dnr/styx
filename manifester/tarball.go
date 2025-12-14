package manifester

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
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
	contents []byte
}

func (b *ManifestBuilder) BuildFromTarball(
	ctx context.Context,
	upstream string,
	shardTotal, shardIndex int,
	useLocalStoreDump string,
	writeBuildRoot bool,
) (*ManifestBuildRes, error) {
	log.Println("manifest tarball", upstream)

	var narOut io.Reader
	var dump *exec.Cmd
	var resolved string
	if useLocalStoreDump != "" {
		dump = exec.CommandContext(ctx, common.NixBin+"-store", "--dump", useLocalStoreDump)
		var err error
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
		// assume caller already resolved redirections
		resolved = upstream
	} else {
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
		resolved = res.Request.URL.String()

		// log.Println("req", storePathHash, "downloading nar")

		switch {
		case strings.HasSuffix(resolved, ".gz") || strings.HasSuffix(resolved, ".tgz"):
			decompress = exec.Command(common.GzipBin, "-d")
		case strings.HasSuffix(resolved, ".xz") || strings.HasSuffix(resolved, ".txz"):
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

		// extract tar into memory
		// TODO: allow passing in expected hash somehow
		tarEnts, err := b.extractTar(tarOut)
		if err != nil {
			return nil, fmt.Errorf("%w: tar read error: %w", ErrInternal, err)
		}

		// ensure we got the whole thing
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

		// construct nar from contents, write to hasher and builder
		pr, pw := io.Pipe()
		go b.writeNar(tarEnts, pw)
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
	spName := getSpNameFromUrl(resolved)
	innerHash := hex.EncodeToString(narHasher.Digest())
	fpHasher := sha256.New()
	// "source" is specific to nar hashing method. we don't support flat here yet.
	fmt.Fprintf(fpHasher, "source:sha256:%s:%s:%s", innerHash, storepath.StoreDir, spName)
	cmpHash := hash.CompressHash(fpHasher.Sum(nil), storepath.PathHashSize)
	sph := nixbase32.EncodeToString(cmpHash)

	log.Println("manifest tarball", upstream, "->", resolved, "built manifest", sph)

	b.stats.Shards.Add(1)

	// if we're not shard 0, we're done
	if shardIndex != 0 {
		return nil, nil
	}

	// add metadata

	nipb := &pb.NarInfo{
		StorePath:   storepath.StoreDir + "/" + sph + "-" + spName,
		Url:         "nar/dummy.nar",
		Compression: "none",
		FileHash:    narHasher.NixString(),
		FileSize:    int64(narHasher.BytesWritten()),
		NarHash:     narHasher.NixString(),
		NarSize:     int64(narHasher.BytesWritten()),
	}
	manifest.Meta = &pb.ManifestMeta{
		GenericTarballOriginal: upstream,
		GenericTarballResolved: resolved,
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
		Upstream:      resolved,
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
				ManifestUpstream: resolved,
				ManifestSph:      ModeGenericTarball,
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

	log.Println("manifest tarball", resolved, "added to cache as", cacheKey)
	b.stats.Manifests.Add(1)

	return &ManifestBuildRes{
		CacheKey: cacheKey,
		Sph:      sph,
		Bytes:    cmpSb,
	}, nil
}

func (b *ManifestBuilder) extractTar(r io.Reader) ([]*tarEntry, error) {
	tr := tar.NewReader(r)

	var ents []*tarEntry
	for {
		ent1, ent2, err := b.tarEntry(tr, len(ents))
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if ent1 != nil {
			ents = append(ents, ent1)
		}
		if ent2 != nil {
			ents = append(ents, ent2)
		}
	}

	// sort in nar order
	sort.Slice(ents, func(i, j int) bool {
		ap := strings.Split(ents[i].Path, "/")
		bp := strings.Split(ents[j].Path, "/")
		for i := range min(len(ap), len(bp)) {
			if ap[i] < bp[i] {
				return true
			} else if ap[i] > bp[i] {
				return false
			}
		}
		return len(ap) < len(bp)
	})

	// do what fetchzip stripRoot does
	ents = stripRoot(ents)

	return ents, nil
}

func (b *ManifestBuilder) tarEntry(tr *tar.Reader, lenSoFar int) (*tarEntry, *tarEntry, error) {
	h, err := tr.Next()
	if err != nil { // including io.EOF
		return nil, nil, err
	}

	name := path.Clean(h.Name)
	if name == "." {
		name = "/"
	} else {
		name = "/" + strings.Trim(name, "/")
	}

	var fakeEnt *tarEntry

	if name != "/" && lenSoFar == 0 {
		// add fake root entry
		fakeEnt = &tarEntry{
			Header: nar.Header{
				Path: "/",
				Type: nar.TypeDirectory,
			},
		}
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
		e.contents, err = io.ReadAll(tr)
		if err != nil {
			return nil, nil, err
		}
		if e.Size != int64(len(e.contents)) {
			return nil, nil, fmt.Errorf("tar regular file size mismatch %q %d != %d",
				h.Name, e.Size, len(e.contents))
		}

	case tar.TypeSymlink:
		e.Type = nar.TypeSymlink
		e.Size = 0
		e.LinkTarget = h.Linkname

	// TODO: hard link?

	default:
		return nil, nil, errors.New("unknown type")
	}

	return fakeEnt, e, nil
}

func (b *ManifestBuilder) writeNar(ents []*tarEntry, w *io.PipeWriter) (retErr error) {
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
			_, err = nw.Write(e.contents)
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

var reNixExprs = regexp.MustCompile(`^https://releases\.nixos\.org/.*/(nix(os|pkgs)-\d\d\.\d\d(\.|pre)\d+).[a-z0-9]+/nixexprs\.tar`)

func getSpNameFromUrl(url string) string {
	// hack: tweak name, e.g. we want
	//   https://releases.nixos.org/nixos/25.11/nixos-25.11.1056.d9bc5c7dceb3/nixexprs.tar.xz
	// to turn into "nixexprs-nixos-25.11.1056" for better diffing
	if m := reNixExprs.FindStringSubmatch(url); m != nil {
		return "nixexprs-" + m[1]
	}
	return path.Base(url)
}

package ci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

const (
	buildRootPrefix = "buildroot/"
)

type (
	gc struct {
		now     time.Time
		stage   func(any)
		summary *strings.Builder
		zp      *common.ZstdCtxPool
		s3      *s3.Client
		bucket  string
		age     time.Duration

		toDelete     sync.Map
		toDelSize    atomic.Int64
		goodNi       sync.Map
		goodNar      sync.Map
		goodManifest sync.Map
		goodChunk    sync.Map
	}
)

func (gc *gc) logln(args ...any)            { fmt.Fprintln(gc.summary, args...) }
func (gc *gc) logf(msg string, args ...any) { fmt.Fprintf(gc.summary, msg+"\n", args...) }

func (gc *gc) writeBuildRoot(ctx context.Context, br *pb.BuildRoot, key string) error {
	data, err := proto.Marshal(br)
	if err != nil {
		return err
	}

	key = buildRootPrefix + key
	z := gc.zp.Get()
	defer gc.zp.Put(z)
	d, err := z.Compress(nil, data)
	if err != nil {
		return err
	}
	_, err = gc.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:          &gc.bucket,
		Key:             &key,
		Body:            bytes.NewReader(d),
		CacheControl:    aws.String("public, max-age=31536000"),
		ContentType:     aws.String("application/octet-stream"),
		ContentEncoding: aws.String("zstd"),
	})
	return err
}

func (gc *gc) readOne(ctx context.Context, key string, dst []byte) ([]byte, error) {
	res, err := gc.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &gc.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if aws.ToString(res.ContentEncoding) == "zstd" {
		z := gc.zp.Get()
		defer gc.zp.Put(z)
		if dst == nil {
			body, err = z.Decompress(nil, body)
		} else {
			var n int
			n, err = z.DecompressInto(dst, body)
			if err == nil {
				body = dst[:n]
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func (gc *gc) listPrefix(ctx context.Context, prefix string, f func(s3types.Object) error) error {
	var token *string
	for {
		res, err := gc.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            &gc.bucket,
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return err
		}
		for _, c := range res.Contents {
			if err := f(c); err != nil {
				return err
			}
		}
		if res.NextContinuationToken == nil {
			return nil
		}
		token = res.NextContinuationToken
	}
}

func (gc *gc) run(ctx context.Context) error {
	if roots, err := gc.loadRoots(ctx); err != nil {
		return err
	} else if err := gc.trace(ctx, roots); err != nil {
		return err
	} else if err := gc.list(ctx); err != nil {
		return err
	} else if err := gc.remove(ctx); err != nil {
		return err
	} else {
		return nil
	}
}

func (gc *gc) del(path string, size int64) {
	gc.toDelete.Store(path, struct{}{})
	gc.toDelSize.Add(size)
}

func (gc *gc) loadRoots(ctx context.Context) ([]string, error) {
	gc.stage("gc load roots")
	var roots []string
	err := gc.listPrefix(ctx, buildRootPrefix, func(o s3types.Object) error {
		key := aws.ToString(o.Key)
		base := path.Base(key)
		parts := strings.Split(base, "@") // "build", time, relid, styx commit
		if len(parts) < 4 {
			log.Println("bad buildroot key", base)
		} else if tm, err := time.Parse(time.RFC3339, parts[1]); err != nil {
			log.Println("bad buildroot key", base, "time parse error", err)
		} else if gc.now.Sub(tm) > gc.age {
			log.Println("build root", base, "too old, gcing")
			gc.del(key, aws.ToInt64(o.Size))
		} else {
			log.Println("using build root", base)
			roots = append(roots, base)
		}
		return nil
	})
	return roots, err
}

func (gc *gc) trace(ctx context.Context, roots []string) error {
	gc.stage("gc trace")

	eg := errgroup.WithContext(ctx)
	eg.SetLimit(50)
	for _, key := range roots {
		key := key
		eg.Go(func() error { return gc.traceRoot(eg, key) })
	}
	return eg.Wait()
}

func (gc *gc) traceRoot(eg *errgroup.Group, key string) error {
	var root pb.BuildRoot
	if b, err := gc.readOne(eg, key, nil); err != nil {
		return err
	} else if err = proto.Unmarshal(b, &root); err != nil {
		return err
	}
	log.Printf("tracing build root %s, %d sph, %d manifest", root, len(root.StorePathHash), len(root.Manifest))
	for _, sph := range root.StorePathHash {
		eg.Go(func() error { return gc.traceSph(eg, sph) })
	}
	for _, mc := range root.Manifest {
		eg.Go(func() error { return gc.traceManifest(eg, mc) })
	}
	return nil
}

func (gc *gc) traceSph(eg *errgroup.Group, sph string) error {
	// TODO: "nixcache" should be derived from configuratoin
	key := "nixcache/" + sph + ".narinfo"
	b, err := gc.readOne(eg, key, nil)
	if err != nil {
		if isNotFound(err) {
			return nil // ignore if not found
		}
		return err
	}
	ni, err := narinfo.Parse(bytes.NewReader(b))
	if err != nil {
		return err
	}
	gc.goodNi.Store(sph, struct{}{})
	if path.Dir(ni.URL) != "nar" {
		return errors.New("unexpected nar url " + ni.URL)
	}
	gc.goodNar.Store(path.Base(ni.URL), struct{}{})
	return nil
}

func (gc *gc) traceManifest(eg *errgroup.Group, mc string) error {
	key := manifester.ManifestCachePath[1:] + mc

	b, err := gc.readOne(eg, key, nil)
	if err != nil {
		if isNotFound(err) {
			return nil // ignore if not found
		}
		return err
	}
	// don't verify signature, assume it's good
	var sm pb.SignedMessage
	err = proto.Unmarshal(b, &sm)
	if err != nil {
		return err
	}

	b = sm.Msg.InlineData
	if b == nil {
		b = make([]byte, sm.Msg.Size)
		subeg := errgroup.WithContext(eg)
		subeg.SetLimit(5)
		for i, dig := range cdig.FromSliceAlias(sm.Msg.Digests) {
			subeg.Go(func() error {
				key := manifester.ChunkReadPath[1:] + dig.String()
				start := i << common.ChunkShift
				end := min(len(b), (i+1)<<common.ChunkShift)
				_, err := gc.readOne(subeg, key, b[start:end])
				return err
			})
		}
		if err := subeg.Wait(); err != nil {
			return err
		}
	}

	var m pb.Manifest
	err = proto.Unmarshal(b, &m)
	if err != nil {
		return err
	}
	for _, ent := range m.Entries {
		for _, dig := range cdig.FromSliceAlias(ent.Digests) {
			gc.goodChunk.Store(dig, struct{}{})
		}
	}
	return nil
}

func (gc *gc) list(ctx context.Context) error {
	gc.stage("gc list")
	return nil
}

func (gc *gc) remove(ctx context.Context) error {
	gc.stage("gc remove")
	return nil
}

func isNotFound(err error) bool {
	var notFound *s3types.NotFound
	return errors.As(err, &notFound)
}

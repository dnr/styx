package ci

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"slices"
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
		stage   func(string)
		summary *strings.Builder
		zp      *common.ZstdCtxPool
		s3      *s3.Client
		bucket  string
		age     time.Duration
		lim     struct{ trace, chunk, list, del, batch int }

		toDelete     sync.Map
		delCount     atomic.Int64
		delSize      atomic.Int64
		totalCount   atomic.Int64
		totalSize    atomic.Int64
		goodNi       sync.Map
		goodNar      sync.Map
		goodManifest sync.Map
		goodChunk    sync.Map
	}

	GCConfig struct {
		Bucket string
		MaxAge time.Duration
	}
)

func GCLocal(ctx context.Context, cfg GCConfig) error {
	var sb strings.Builder
	s3, err := getS3Cli()
	if err != nil {
		return err
	}
	gc := gc{
		now:     time.Now(),
		stage:   func(s string) { log.Println("======================", "STAGE", s) },
		summary: &sb,
		zp:      common.GetZstdCtxPool(),
		s3:      s3,
		bucket:  cfg.Bucket,
		age:     cfg.MaxAge,
		lim: struct{ trace, chunk, list, del, batch int }{
			trace: 10,
			chunk: 3,
			list:  5,
			del:   10,
			batch: 100,
		},
	}
	return gc.run(ctx)
}

func (gc *gc) logln(args ...any) {
	log.Println(args...)
	fmt.Fprintln(gc.summary, args...)
}
func (gc *gc) logf(msg string, args ...any) {
	log.Printf(msg, args...)
	fmt.Fprintf(gc.summary, msg+"\n", args...)
}

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
	start := time.Now()
	if roots, err := gc.loadRoots(ctx); err != nil {
		gc.logln("gc loadRoots error:", err)
		return err
	} else if err := gc.trace(ctx, roots); err != nil {
		gc.logln("gc trace error:", err)
		return err
	} else if err := gc.list(ctx); err != nil {
		gc.logln("gc list error:", err)
		return err
	} else if err := gc.remove(ctx); err != nil {
		gc.logln("gc remove error:", err)
		return err
	}
	gc.logf("gc done in %s", time.Since(start))
	return nil
}

func (gc *gc) del(path string, size int64) {
	gc.toDelete.Store(path, struct{}{})
	gc.delCount.Add(1)
	gc.delSize.Add(size)
}

func (gc *gc) loadRoots(ctx context.Context) ([]string, error) {
	gc.stage("GC LOAD ROOTS")
	var roots []string
	err := gc.listPrefix(ctx, buildRootPrefix, func(o s3types.Object) error {
		key := aws.ToString(o.Key)
		gc.totalCount.Add(1)
		gc.totalSize.Add(aws.ToInt64(o.Size))
		base := path.Base(key)
		parts := strings.Split(base, "@") // "build", time, relid, styx commit
		keep := false
		if len(parts) < 4 {
			gc.logln("bad root key", base)
		} else if tm, err := time.Parse(time.RFC3339, parts[1]); err != nil {
			gc.logln("bad root key", base, "time parse error", err)
		} else if gc.now.Sub(tm) > gc.age {
			gc.logln("stale root", base)
		} else {
			log.Println("using root", base)
			keep = true
			roots = append(roots, base)
		}
		if !keep {
			gc.del(key, aws.ToInt64(o.Size))
		}
		return nil
	})
	return roots, err
}

func (gc *gc) trace(ctx context.Context, roots []string) error {
	gc.stage("GC TRACE")

	eg := errgroup.WithContext(ctx)
	eg.SetLimit(cmp.Or(gc.lim.trace, 50))
	for _, key := range roots {
		key := key
		eg.Go(func() error { return gc.traceRoot(eg, key) })
	}
	return eg.Wait()
}

func (gc *gc) traceRoot(eg *errgroup.Group, key string) error {
	var root pb.BuildRoot
	if b, err := gc.readOne(eg, buildRootPrefix+key, nil); err != nil {
		return err
	} else if err = proto.Unmarshal(b, &root); err != nil {
		return err
	}
	log.Printf("tracing root %s, %d sph, %d manifest", key, len(root.StorePathHash), len(root.Manifest))
	for _, sph := range root.StorePathHash {
		eg.Go(func() error { return gc.traceSph(eg, sph) })
	}
	for _, mc := range root.Manifest {
		eg.Go(func() error { return gc.traceManifest(eg, mc) })
	}
	return nil
}

func (gc *gc) traceSph(eg *errgroup.Group, sph string) error {
	// TODO: "nixcache" should be derived from configuration
	key := "nixcache/" + sph + ".narinfo"
	b, err := gc.readOne(eg, key, nil)
	if err != nil {
		if manifester.IsS3NotFound(err) {
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
	log.Printf("traced %s = %s", key, path.Base(ni.StorePath)[33:])
	return nil
}

func (gc *gc) traceManifest(eg *errgroup.Group, mc string) error {
	key := manifester.ManifestCachePath[1:] + mc

	b, err := gc.readOne(eg, key, nil)
	if err != nil {
		if manifester.IsS3NotFound(err) {
			return nil // ignore if not found
		}
		return err
	}
	gc.goodManifest.Store(mc, struct{}{})
	// don't verify signature, assume it's good
	var sm pb.SignedMessage
	err = proto.Unmarshal(b, &sm)
	if err != nil {
		return err
	}

	var chunks, mchunks int
	b = sm.Msg.InlineData
	if b == nil {
		b = make([]byte, sm.Msg.Size)
		subeg := errgroup.WithContext(eg)
		subeg.SetLimit(cmp.Or(gc.lim.chunk, 5))
		for i, dig := range cdig.FromSliceAlias(sm.Msg.Digests) {
			gc.goodChunk.Store(dig, struct{}{})
			mchunks++
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
			chunks++
		}
	}
	log.Printf("traced %s = %s, %d chunks, %d manifest chunks",
		key, path.Base(m.Meta.Narinfo.StorePath)[33:], chunks, mchunks)
	return nil
}

func (gc *gc) list(ctx context.Context) error {
	gc.stage("GC LIST")
	eg := errgroup.WithContext(ctx)
	eg.SetLimit(cmp.Or(gc.lim.list, 5))
	eg.Go(func() error {
		var count, size int64
		err := gc.listPrefix(eg, "nixcache/", func(o s3types.Object) error {
			count++
			key := aws.ToString(o.Key)
			size += aws.ToInt64(o.Size)
			if rest, ok := strings.CutPrefix(key, "nixcache/nar/"); ok {
				if _, ok := gc.goodNar.Load(rest); !ok {
					gc.del(key, aws.ToInt64(o.Size))
				}
			} else if rest, ok := strings.CutSuffix(key, ".narinfo"); ok {
				rest = strings.TrimPrefix(rest, "nixcache/")
				if _, ok := gc.goodNi.Load(rest); !ok {
					gc.del(key, aws.ToInt64(o.Size))
				}
			} else if key == "nixcache/nix-cache-info" {
				// leave
			} else {
				gc.logln("unexpected file in nix cache", key)
				gc.del(key, aws.ToInt64(o.Size))
			}
			return nil
		})
		gc.totalCount.Add(count)
		gc.totalSize.Add(size)
		return err
	})
	eg.Go(func() error {
		var count, size int64
		err := gc.listPrefix(eg, manifester.ManifestCachePath[1:], func(o s3types.Object) error {
			count++
			key := aws.ToString(o.Key)
			size += aws.ToInt64(o.Size)
			if _, ok := gc.goodManifest.Load(path.Base(key)); !ok {
				gc.del(key, aws.ToInt64(o.Size))
			}
			return nil
		})
		gc.totalCount.Add(count)
		gc.totalSize.Add(size)
		return err
	})
	for _, pchar := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" {
		pchar := pchar
		eg.Go(func() error {
			var count, size int64
			err := gc.listPrefix(eg, manifester.ChunkReadPath[1:]+string(pchar), func(o s3types.Object) error {
				count++
				key := aws.ToString(o.Key)
				size += aws.ToInt64(o.Size)
				gc.totalCount.Add(1)
				gc.totalSize.Add(aws.ToInt64(o.Size))
				b, err := base64.RawURLEncoding.DecodeString(path.Base(key))
				if err != nil {
					gc.logln("unexpected file in chunk store", key)
					gc.del(key, aws.ToInt64(o.Size))
				} else if _, ok := gc.goodChunk.Load(cdig.FromBytes(b)); !ok {
					gc.del(key, aws.ToInt64(o.Size))
				}
				return nil
			})
			gc.totalCount.Add(count)
			gc.totalSize.Add(size)
			return err
		})
	}
	return eg.Wait()
}

func (gc *gc) remove(ctx context.Context) error {
	gc.stage("GC REMOVE")

	gc.logf("total : %9d objects, %14d bytes", gc.totalCount.Load(), gc.totalSize.Load())
	gc.logf("remove: %9d objects, %14d bytes", gc.delCount.Load(), gc.delSize.Load())
	gc.logf("keep  : %9d objects, %14d bytes", gc.totalCount.Load()-gc.delCount.Load(), gc.totalSize.Load()-gc.delSize.Load())

	delerrors := 0
	eg := errgroup.WithContext(ctx)
	eg.SetLimit(cmp.Or(gc.lim.del, 20))
	bsize := cmp.Or(gc.lim.batch, 100)
	var batch []string
	flush := func() {
		if len(batch) == 0 {
			return
		}
		del := makeBatchDelete(batch)
		batch = batch[:0]
		eg.Go(func() error {
			res, err := gc.s3.DeleteObjects(eg, &s3.DeleteObjectsInput{
				Bucket: &gc.bucket,
				Delete: del,
			})
			if res != nil {
				delerrors += len(res.Errors)
			}
			return err
		})
	}
	gc.toDelete.Range(func(k, _ any) bool {
		batch = append(batch, k.(string))
		if len(batch) >= bsize {
			flush()
		}
		return true
	})
	flush()
	err := eg.Wait()
	if delerrors > 0 {
		gc.logf("delete errors: %d", delerrors)
	}
	return err
}

func makeBatchDelete(keys []string) *s3types.Delete {
	keys = slices.Clone(keys)
	objs := make([]s3types.ObjectIdentifier, len(keys))
	for i := range keys {
		objs[i].Key = &keys[i]
	}
	return &s3types.Delete{
		Objects: objs,
		Quiet:   aws.Bool(true),
	}
}

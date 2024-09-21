package ci

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/pb"
)

const (
	buildRootPrefix = "buildroot/"
)

type (
	gc struct {
		ctx     context.Context
		now     time.Time
		stage   func(any)
		summary *strings.Builder
		zp      *common.ZstdCtxPool
		s3      *s3.Client
		bucket  string
		age     time.Duration

		toDelete     []string
		toDelSize    int64
		goodRoots    []string
		goodNi       map[string]struct{}
		goodNar      map[string]struct{}
		goodManifest map[string]struct{}
		goodChunk    map[string]struct{}
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

func (gc *gc) run() error {
	if err := gc.prune(); err != nil {
		return err
	} else if err := gc.trace(); err != nil {
		return err
	} else if err := gc.list(); err != nil {
		return err
	} else if err := gc.remove(); err != nil {
		return err
	} else {
		return nil
	}
}

func (gc *gc) del(path string, size int64) {
	gc.toDelete = append(gc.toDelete, path)
	gc.toDelSize += size
}

func (gc *gc) prune() error {
	gc.stage("gc prune")
	var token *string
	for {
		res, err := gc.s3.ListObjectsV2(gc.ctx, &s3.ListObjectsV2Input{
			Bucket:            &gc.bucket,
			Prefix:            aws.String(buildRootPrefix),
			ContinuationToken: token,
		})
		if err != nil {
			return err
		}

		for _, c := range res.Contents {
			base := path.Base(*c.Key)
			parts := strings.Split(base, "@") // "build", time, relid, styx commit
			if len(parts) < 4 {
				log.Println("bad buildroot key", base)
			} else if tm, err := time.Parse(time.RFC3339, parts[1]); err != nil {
				log.Println("bad buildroot key", base, "time parse error", err)
			} else if gc.now.Sub(tm) > gc.age {
				log.Println("build root", base, "too old, gcing")
				gc.del(*c.Key, *c.Size)
			} else {
				log.Println("using build root", base)
				gc.goodRoots = append(gc.goodRoots, base)
			}
		}

		if res.NextContinuationToken == nil {
			break
		}
		token = res.NextContinuationToken
	}
	return nil
}

func (gc *gc) trace() error {
	gc.stage("gc trace")

	for _, root := range gc.goodRoots {
		_ = root // FIXME
	}
	return nil
}

func (gc *gc) list() error {
	gc.stage("gc list")
	return nil
}

func (gc *gc) remove() error {
	gc.stage("gc remove")
	return nil
}

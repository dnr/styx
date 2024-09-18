package ci

import (
	"bytes"
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/pb"
)

const (
	buildRootPrefix = "buildroot/"
)

func (a *heavyActivities) writeBuildRoot(ctx context.Context, br *pb.BuildRoot, key string) error {
	data, err := proto.Marshal(br)
	if err != nil {
		return err
	}

	key = buildRootPrefix + key
	z := a.zp.Get()
	defer a.zp.Put(z)
	d, err := z.Compress(nil, data)
	if err != nil {
		return err
	}
	_, err = a.s3cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket:          &a.cfg.CSWCfg.ChunkBucket,
		Key:             &key,
		Body:            bytes.NewReader(d),
		CacheControl:    aws.String("public, max-age=31536000"),
		ContentType:     aws.String("application/octet-stream"),
		ContentEncoding: aws.String("zstd"),
	})
	return err
}

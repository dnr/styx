package manifester

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type (
	chunkStore interface {
		PutIfNotExists(ctx context.Context, key string, data func() []byte) error
	}

	localDirChunkStore struct {
		dir string
	}

	s3ChunkStore struct {
		bucket   string
		s3client *s3.Client
	}
)

func newLocalDirChunkStore(dir string) (*localDirChunkStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &localDirChunkStore{dir: dir}, nil
}

func (l *localDirChunkStore) PutIfNotExists(ctx context.Context, key string, data func() []byte) error {
	fn := path.Join(l.dir, key)
	if _, err := os.Stat(fn); err == nil {
		return nil
	}
	d := data()
	tmpfn := fn + ".tmp"
	if err := os.WriteFile(tmpfn, d, 0644); err != nil {
		_ = os.Remove(tmpfn)
		return err
	}
	if err := os.Rename(tmpfn, fn); err != nil {
		_ = os.Remove(tmpfn)
		return err
	}
	return nil
}

func newS3ChunkStore(bucket string) (*s3ChunkStore, error) {
	awscfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}
	s3client := s3.NewFromConfig(awscfg, func(o *s3.Options) {
		o.EndpointOptions.DisableHTTPS = true
	})
	return &s3ChunkStore{
		bucket:   bucket,
		s3client: s3client,
	}, nil
}

func (s *s3ChunkStore) PutIfNotExists(ctx context.Context, key string, data func() []byte) error {
	_, err := s.s3client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	var notFound *s3types.NotFound
	if err == nil || !errors.As(err, &notFound) {
		return err
	}
	_, err = s.s3client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:          &s.bucket,
		Key:             &key,
		Body:            bytes.NewReader(data()),
		CacheControl:    aws.String("public, max-age=31536000"),
		ContentType:     aws.String("application/octet-stream"),
		ContentEncoding: aws.String("zstd"),
	})
	return err
}

func makeChunkStore(cfg *Config) (chunkStore, error) {
	if len(cfg.ChunkLocalDir) > 0 {
		return newLocalDirChunkStore(cfg.ChunkLocalDir)
	} else if len(cfg.ChunkBucket) > 0 {
		return newS3ChunkStore(cfg.ChunkBucket)
	}
	return nil, errors.New("chunk store configuration is missing")
}

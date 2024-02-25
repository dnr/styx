package manifester

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/klauspost/compress/zstd"
)

type (
	ChunkStoreWrite interface {
		PutIfNotExists(ctx context.Context, key string, data []byte) error
	}

	ChunkStoreRead interface {
		Get(ctx context.Context, key string) ([]byte, error)
	}

	ChunkStoreWriteConfig struct {
		// One of these is required:
		ChunkBucket   string
		ChunkLocalDir string
	}

	ChunkStoreReadConfig struct {
		// One of these is required:
		ChunkBucket   string
		ChunkLocalDir string
	}

	localChunkStoreWrite struct {
		dir string
		enc *zstd.Encoder
	}

	localChunkStoreRead struct {
		dir string
		dec *zstd.Decoder
	}

	s3ChunkStoreWrite struct {
		bucket   string
		s3client *s3.Client
		enc      *zstd.Encoder
	}

	s3ChunkStoreRead struct {
		bucket   string
		s3client *s3.Client
		dec      *zstd.Decoder
	}
)

func newLocalChunkStoreWrite(dir string, enc *zstd.Encoder) (*localChunkStoreWrite, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &localChunkStoreWrite{dir: dir, enc: enc}, nil
}

func (l *localChunkStoreWrite) PutIfNotExists(ctx context.Context, key string, data []byte) error {
	fn := path.Join(l.dir, key)
	if _, err := os.Stat(fn); err == nil {
		return nil
	}
	d := l.enc.EncodeAll(data, nil)
	if out, err := os.CreateTemp(l.dir, key+".tmp*"); err != nil {
		return err
	} else if n, err := out.Write(d); err != nil || n != len(d) {
		_ = out.Close()
		_ = os.Remove(out.Name())
		return err
	} else if err := out.Close(); err != nil {
		_ = os.Remove(out.Name())
		return err
	} else if err := os.Rename(out.Name(), fn); err != nil {
		_ = os.Remove(out.Name())
		return err
	}
	return nil
}

func newLocalChunkStoreRead(dir string, dec *zstd.Decoder) (*localChunkStoreRead, error) {
	return &localChunkStoreRead{dir: dir, dec: dec}, nil
}

func (l *localChunkStoreRead) Get(ctx context.Context, key string) ([]byte, error) {
	fn := path.Join(l.dir, key)
	b, err := os.ReadFile(fn)
	if err != nil {
		return nil, err
	}
	return l.dec.DecodeAll(b, nil)

}

func newS3ChunkStoreWrite(bucket string, enc *zstd.Encoder) (*s3ChunkStoreWrite, error) {
	awscfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}
	s3client := s3.NewFromConfig(awscfg, func(o *s3.Options) {
		o.EndpointOptions.DisableHTTPS = true
	})
	return &s3ChunkStoreWrite{
		bucket:   bucket,
		s3client: s3client,
		enc:      enc,
	}, nil
}

func (s *s3ChunkStoreWrite) PutIfNotExists(ctx context.Context, key string, data []byte) error {
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
		Body:            bytes.NewReader(s.enc.EncodeAll(data, nil)),
		CacheControl:    aws.String("public, max-age=31536000"),
		ContentType:     aws.String("application/octet-stream"),
		ContentEncoding: aws.String("zstd"),
	})
	return err
}

func newS3ChunkStoreRead(bucket string, dec *zstd.Decoder) (*s3ChunkStoreRead, error) {
	awscfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}
	s3client := s3.NewFromConfig(awscfg, func(o *s3.Options) {
		o.EndpointOptions.DisableHTTPS = true
	})
	return &s3ChunkStoreRead{
		bucket:   bucket,
		s3client: s3client,
		dec:      dec,
	}, nil
}

func (s *s3ChunkStoreRead) Get(ctx context.Context, key string) ([]byte, error) {
	res, err := s.s3client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if aws.ToString(res.ContentEncoding) == "zstd" {
		b, err = s.dec.DecodeAll(b, nil)
	}
	return b, err
}

func NewChunkStoreWrite(cfg ChunkStoreWriteConfig) (ChunkStoreWrite, error) {
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderCRC(false),
	)
	if err != nil {
		return nil, err
	}
	if len(cfg.ChunkLocalDir) > 0 {
		return newLocalChunkStoreWrite(cfg.ChunkLocalDir, enc)
	} else if len(cfg.ChunkBucket) > 0 {
		return newS3ChunkStoreWrite(cfg.ChunkBucket, enc)
	}
	return nil, errors.New("chunk store configuration is missing")
}

func NewChunkStoreRead(cfg ChunkStoreReadConfig) (ChunkStoreRead, error) {
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderMaxMemory(1<<20), // chunks are small
	)
	if err != nil {
		return nil, err
	}
	if len(cfg.ChunkLocalDir) > 0 {
		return newLocalChunkStoreRead(cfg.ChunkLocalDir, dec)
	} else if len(cfg.ChunkBucket) > 0 {
		return newS3ChunkStoreRead(cfg.ChunkBucket, dec)
	}
	return nil, errors.New("chunk store configuration is missing")
}

package manifester

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	s3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/klauspost/compress/zstd"
)

type (
	ChunkStoreWrite interface {
		PutIfNotExists(ctx context.Context, path, key string, data []byte) error
	}

	ChunkStoreRead interface {
		// Data will be appended to dst and returned
		Get(ctx context.Context, key string, dst []byte) ([]byte, error)
	}

	ChunkStoreWriteConfig struct {
		// One of these is required:
		ChunkBucket   string
		ChunkLocalDir string
	}

	localChunkStoreWrite struct {
		dir string
		enc *zstd.Encoder
	}

	s3ChunkStoreWrite struct {
		bucket   string
		s3client *s3.Client
		enc      *zstd.Encoder
	}

	urlChunkStoreRead struct {
		url string
		dec *zstd.Decoder
	}
)

func newLocalChunkStoreWrite(dir string, enc *zstd.Encoder) (*localChunkStoreWrite, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &localChunkStoreWrite{dir: dir, enc: enc}, nil
}

func (l *localChunkStoreWrite) PutIfNotExists(ctx context.Context, path_, key string, data []byte) error {
	// ignore path! mix chunks and manifests in same directory
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

func (s *s3ChunkStoreWrite) PutIfNotExists(ctx context.Context, path, key string, data []byte) error {
	if path != ChunkReadPath && path != ManifestCachePath {
		panic("path must be ChunkReadPath or ManifestCachePath")
	}
	key = path[1:] + key
	_, err := s.s3client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	var notFound *s3types.NotFound
	if err == nil || !errors.As(err, &notFound) {
		return err
	}
	_, err = s.s3client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:          &s.bucket,
		Key:             &key,
		Body:            bytes.NewReader(s.enc.EncodeAll(data, nil)),
		CacheControl:    aws.String("public, max-age=31536000"),
		ContentType:     aws.String("application/octet-stream"),
		ContentEncoding: aws.String("zstd"),
	})
	return err
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

// path should be either ChunkReadPath or ManifestCachePath
func NewChunkStoreReadUrl(url, path string) ChunkStoreRead {
	if path != ChunkReadPath && path != ManifestCachePath {
		panic("path must be ChunkReadPath or ManifestCachePath")
	}
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderMaxMemory(1<<20), // chunks are small
	)
	if err != nil {
		panic("can't create zstd decoder")
	}
	return &urlChunkStoreRead{
		url: strings.TrimSuffix(url, "/") + path,
		dec: dec,
	}
}

func (s *urlChunkStoreRead) Get(ctx context.Context, key string, dst []byte) ([]byte, error) {
	url := s.url + key
	res, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http error: %s", res.Status)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.Header.Get("Content-Encoding") == "zstd" {
		return s.dec.DecodeAll(b, dst)
	} else {
		return append(dst, b...), nil
	}
}

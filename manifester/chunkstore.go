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

	"github.com/dnr/styx/common"
)

type (
	ChunkStoreWrite interface {
		PutIfNotExists(ctx context.Context, path, key string, data []byte) ([]byte, error)
		Get(ctx context.Context, path, key string, dst []byte) ([]byte, error)
	}

	ChunkStoreRead interface {
		// Data will be appended to dst and returned.
		// If dst is given, it must be big enough to hold the full chunk!
		Get(ctx context.Context, key string, dst []byte) ([]byte, error)
	}

	ChunkStoreWriteConfig struct {
		// One of these is required:
		ChunkBucket      string
		ChunkLocalDir    string
		ZstdEncoderLevel int
	}

	localChunkStoreWrite struct {
		dir string
		zp  *common.ZstdCtxPool
	}

	s3ChunkStoreWrite struct {
		bucket   string
		s3client *s3.Client
		zp       *common.ZstdCtxPool
		zlevel   int
	}

	urlChunkStoreRead struct {
		url string
		zp  *common.ZstdCtxPool
	}
)

func newLocalChunkStoreWrite(dir string) (*localChunkStoreWrite, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &localChunkStoreWrite{dir: dir, zp: common.GetZstdCtxPool()}, nil
}

func (l *localChunkStoreWrite) PutIfNotExists(ctx context.Context, path_, key string, data []byte) ([]byte, error) {
	// ignore path! mix chunks and manifests in same directory
	z := l.zp.Get()
	defer l.zp.Put(z)
	fn := path.Join(l.dir, key)
	if _, err := os.Stat(fn); err == nil {
		return nil, nil
	} else if d, err := z.CompressLevel(nil, data, 1); err != nil {
		return nil, err
	} else if out, err := os.CreateTemp(l.dir, key+".tmp*"); err != nil {
		return nil, err
	} else if n, err := out.Write(d); err != nil || n != len(d) {
		_ = out.Close()
		_ = os.Remove(out.Name())
		return nil, err
	} else if err := out.Close(); err != nil {
		_ = os.Remove(out.Name())
		return nil, err
	} else if err := os.Rename(out.Name(), fn); err != nil {
		_ = os.Remove(out.Name())
		return nil, err
	} else {
		return d, nil
	}
}

func (l *localChunkStoreWrite) Get(ctx context.Context, path_, key string, data []byte) ([]byte, error) {
	// ignore path! mix chunks and manifests in same directory
	b, err := os.ReadFile(path.Join(l.dir, key))
	if err != nil {
		return nil, err
	}
	z := l.zp.Get()
	defer l.zp.Put(z)
	return z.Decompress(data, b)
}

func newS3ChunkStoreWrite(bucket string, zlevel int) (*s3ChunkStoreWrite, error) {
	awscfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithEC2IMDSRegion())
	if err != nil {
		return nil, err
	}
	s3client := s3.NewFromConfig(awscfg, func(o *s3.Options) {
		o.EndpointOptions.DisableHTTPS = true
	})
	return &s3ChunkStoreWrite{
		bucket:   bucket,
		s3client: s3client,
		zp:       common.GetZstdCtxPool(),
		zlevel:   zlevel,
	}, nil
}

func (s *s3ChunkStoreWrite) PutIfNotExists(ctx context.Context, path, key string, data []byte) ([]byte, error) {
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
		return nil, err
	}
	z := s.zp.Get()
	defer s.zp.Put(z)
	// TODO: use buffer pool here (requires caller to return it?)
	d, err := z.CompressLevel(nil, data, s.zlevel)
	if err != nil {
		return nil, err
	}
	_, err = s.s3client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:          &s.bucket,
		Key:             &key,
		Body:            bytes.NewReader(d),
		CacheControl:    aws.String("public, max-age=31536000"),
		ContentType:     aws.String("application/octet-stream"),
		ContentEncoding: aws.String("zstd"),
	})
	return d, err
}

func (s *s3ChunkStoreWrite) Get(ctx context.Context, path, key string, data []byte) ([]byte, error) {
	key = path[1:] + key
	res, err := s.s3client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	z := s.zp.Get()
	defer s.zp.Put(z)
	return z.Decompress(data, b)
}

func NewChunkStoreWrite(cfg ChunkStoreWriteConfig) (ChunkStoreWrite, error) {
	if len(cfg.ChunkLocalDir) > 0 {
		return newLocalChunkStoreWrite(cfg.ChunkLocalDir)
	} else if len(cfg.ChunkBucket) > 0 {
		return newS3ChunkStoreWrite(cfg.ChunkBucket, cfg.ZstdEncoderLevel)
	}
	return nil, errors.New("chunk store configuration is missing")
}

// path should be either ChunkReadPath or ManifestCachePath
func NewChunkStoreReadUrl(url, path string) ChunkStoreRead {
	if path != ChunkReadPath && path != ManifestCachePath {
		panic("path must be ChunkReadPath or ManifestCachePath")
	}
	return &urlChunkStoreRead{
		url: strings.TrimSuffix(url, "/") + path,
		zp:  common.GetZstdCtxPool(),
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
		z := s.zp.Get()
		defer s.zp.Put(z)
		if dst == nil {
			return z.Decompress(nil, b)
		} else {
			// fast path, assume buffer is big enough
			n, err := z.DecompressInto(dst[len(dst):cap(dst)], b)
			if err != nil {
				return nil, err
			}
			return dst[:len(dst)+n], nil
		}
	} else {
		return append(dst, b...), nil
	}
}

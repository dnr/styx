package manifester

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/dnr/styx/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFromTarball(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	files := []struct {
		name, body string
		isDir      bool
	}{
		{"root", "", true},
		{"root/z", "content z", false},
		{"root/a", "content a", false},
		// "root/b" is omitted
		{"root/b/c", "content c", false},
		{"root/b/f", "content f", false},
		{"root/b/d", "content d", false},
	}

	for _, f := range files {
		hdr := &tar.Header{
			Name: f.name,
			Mode: 0644,
		}
		if f.isDir {
			hdr.Typeflag = tar.TypeDir
			hdr.Name += "/"
		} else {
			hdr.Size = int64(len(f.body))
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if !f.isDir {
			_, err := tw.Write([]byte(f.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Etag", `"some-etag"`)
		w.Write(buf.Bytes())
	}))
	defer ts.Close()

	cs := &mockChunkStore{data: make(map[string][]byte)}
	builder, err := NewManifestBuilder(ManifestBuilderConfig{}, cs)
	require.NoError(t, err)

	res, err := builder.BuildFromTarball(context.Background(), ts.URL, 1, 0, "", false)
	require.NoError(t, err)
	assert.NotEmpty(t, res.CacheKey)
	assert.NotEmpty(t, res.Sph)

	// just check the manifest was added to cache
	assert.Contains(t, cs.data, ManifestCachePath+"/"+res.CacheKey)
}

type mockChunkStore struct {
	lock sync.Mutex
	data map[string][]byte
}

func (m *mockChunkStore) PutIfNotExists(ctx context.Context, ns string, key string, data []byte) ([]byte, error) {
	m.lock.Lock()
	fullKey := ns + "/" + key
	if _, ok := m.data[fullKey]; ok {
		m.lock.Unlock()
		return nil, nil // already exists
	}
	m.data[fullKey] = data
	m.lock.Unlock()

	// return zstd compressed for PutIfNotExists if it was new
	zp := common.GetZstdCtxPool()
	z := zp.Get()
	defer zp.Put(z)
	return z.Compress(nil, data)
}

func (m *mockChunkStore) Get(ctx context.Context, ns string, key string, dst []byte) ([]byte, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	fullKey := ns + "/" + key
	data, ok := m.data[fullKey]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append(dst, data...), nil
}

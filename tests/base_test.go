package tests

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phayes/freeport"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/client"
	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

const (
	nixosKeys = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="

	devnode = "/dev/cachefiles"

	blockShift = 12
)

type (
	service interface {
		Stop()
	}

	testBase struct {
		t              *testing.T
		tag            string
		basetmpdir     string
		chunkdir       string
		cachedir       string
		manifesterAddr string
		upstreamHost   string
		upstreamUrl    string

		tdserver   *http.Server
		manifester service
		daemon     service
	}
)

var (
	TestdataDir = "/must-set-with-ldflags"
)

func newTestBase(t *testing.T) *testBase {
	log.SetFlags(log.Lmicroseconds | log.Lshortfile)

	// check uid
	require.Equal(t, 0, os.Getuid(), "tests must be run as root")

	// check nothing else has devnode
	var exitErr *exec.ExitError
	require.ErrorAs(t, exec.Command("fuser", "-s", devnode).Run(), &exitErr,
		"tests require exclusive access to "+devnode)

	tag := fmt.Sprintf("styxtest%x", rand.Uint64())
	t.Log("cache tag/domain", tag)

	basetmpdir := t.TempDir()
	// basetmpdir = "/tmp"
	chunkdir := filepath.Join(basetmpdir, "chunks")
	require.NoError(t, os.Mkdir(chunkdir, 0755))
	cachedir := filepath.Join(basetmpdir, "cache")
	require.NoError(t, os.Mkdir(cachedir, 0755))

	tdport, err := freeport.GetFreePort()
	require.NoError(t, err)

	tb := &testBase{
		t:            t,
		tag:          tag,
		basetmpdir:   basetmpdir,
		chunkdir:     chunkdir,
		cachedir:     cachedir,
		upstreamHost: fmt.Sprintf("localhost:%d", tdport),
		upstreamUrl:  fmt.Sprintf("http://localhost:%d/", tdport),
	}
	t.Cleanup(tb.cleanup)
	return tb
}

func (tb *testBase) cleanup() {
	if tb.manifester != nil {
		tb.t.Log("stopping manifester")
		tb.manifester.Stop()
	}
	if tb.tdserver != nil {
		tb.t.Log("stopping test data server")
		tb.tdserver.Close()
	}
	if tb.daemon != nil {
		tb.t.Log("stopping daemon")
		tb.daemon.Stop()
	}
}

func (tb *testBase) startTestDataServer() {
	// http server acting as binary cache
	tb.tdserver = &http.Server{
		Addr:    tb.upstreamHost,
		Handler: http.FileServer(http.Dir(TestdataDir)),
	}
	go tb.tdserver.ListenAndServe()
}

func (tb *testBase) startManifester() {
	tb.startTestDataServer()

	port, err := freeport.GetFreePort()
	require.NoError(tb.t, err)

	cswcfg := manifester.ChunkStoreWriteConfig{
		ChunkLocalDir: tb.chunkdir,
	}
	cs, err := manifester.NewChunkStoreWrite(cswcfg)
	require.NoError(tb.t, err)

	mbcfg := manifester.ManifestBuilderConfig{
		ConcurrentChunkOps: 10,
	}
	mbcfg.PublicKeys, err = common.LoadPubKeys([]string{nixosKeys})
	require.NoError(tb.t, err)
	mbcfg.SigningKeys, err = common.LoadSecretKeys([]string{"../keys/testsuite.secret"})
	require.NoError(tb.t, err)
	mb, err := manifester.NewManifestBuilder(mbcfg, cs)

	hostport := fmt.Sprintf("localhost:%d", port)
	tb.manifesterAddr = fmt.Sprintf("http://%s/", hostport)
	cfg := manifester.Config{
		Bind:               hostport,
		AllowedUpstreams:   []string{tb.upstreamHost},
		ChunkDiffZstdLevel: 3,
		ChunkDiffParallel:  60,
	}

	m, err := manifester.NewManifestServer(cfg, mb)
	require.NoError(tb.t, err)

	go m.Run()
	tb.t.Log("manifester running on", hostport)
	tb.manifester = m
}

func (tb *testBase) startDaemon() {
	if tb.manifesterAddr == "" {
		tb.t.Error("start manifester before daemon")
	}

	d := daemon.NewServer(daemon.Config{
		DevPath:         devnode,
		CachePath:       tb.cachedir,
		CacheTag:        tb.tag,
		CacheDomain:     tb.tag,
		ErofsBlockShift: blockShift,
		// SmallFileCutoff: 224,
		Workers:         10,
		ReadaheadChunks: 8,
		IsTesting:       true,
	})
	err := d.Start()
	require.NoError(tb.t, err)
	tb.t.Log("daemon running in", tb.cachedir)
	tb.daemon = d
	tb.initDaemon()
}

func (tb *testBase) initDaemon() {
	pk, err := os.ReadFile("../keys/testsuite.public")
	require.NoError(tb.t, err)
	params := pb.DaemonParams{
		Params: &pb.GlobalParams{
			ChunkShift: int32(common.ChunkShift),
			DigestAlgo: common.DigestAlgo,
			DigestBits: cdig.Bits,
		},
		ManifesterUrl:    tb.manifesterAddr,
		ManifestCacheUrl: tb.manifesterAddr,
		ChunkReadUrl:     tb.manifesterAddr,
		ChunkDiffUrl:     tb.manifesterAddr,
	}

	sock := filepath.Join(tb.cachedir, "styx.sock")
	c := client.NewClient(sock)
	var res daemon.Status
	code, err := c.Call(daemon.InitPath, &daemon.InitReq{
		PubKeys: []string{string(pk)},
		Params:  params,
	}, &res)
	require.NoError(tb.t, err)
	require.Equal(tb.t, code, http.StatusOK)
	require.True(tb.t, res.Success, "error:", res.Error)
	tb.t.Log("daemon initialized")
}

func (tb *testBase) mount(storePath string) string {
	mp := tb.t.TempDir()
	tb.t.Cleanup(func() {
		// if the test unmounted already this will just fail
		_ = unix.Unmount(mp, 0)
	})
	sock := filepath.Join(tb.cachedir, "styx.sock")
	c := client.NewClient(sock)
	var res daemon.Status
	code, err := c.Call(daemon.MountPath, daemon.MountReq{
		Upstream:   tb.upstreamUrl,
		StorePath:  storePath,
		MountPoint: mp,
	}, &res)
	require.NoError(tb.t, err)
	require.Equal(tb.t, code, http.StatusOK)
	require.True(tb.t, res.Success, "error:", res.Error)
	return mp
}

func (tb *testBase) umount(storePath string) {
	sock := filepath.Join(tb.cachedir, "styx.sock")
	c := client.NewClient(sock)
	var res daemon.Status
	code, err := c.Call(daemon.UmountPath, daemon.UmountReq{
		StorePath: storePath,
	}, &res)
	require.NoError(tb.t, err)
	require.Equal(tb.t, code, http.StatusOK)
	require.True(tb.t, res.Success, "error:", res.Error)
}

func (tb *testBase) nixHash(path string) string {
	b, err := exec.Command("nix-hash", "--type", "sha256", "--base32", path).Output()
	require.NoErrorf(tb.t, err, "output: %q %v", b, err)
	return strings.TrimSpace(string(b))
}

func (tb *testBase) debug(req ...daemon.DebugReq) *daemon.DebugResp {
	sock := filepath.Join(tb.cachedir, "styx.sock")
	c := client.NewClient(sock)
	var res daemon.DebugResp
	if len(req) == 0 {
		req = append(req, daemon.DebugReq{})
	}
	code, err := c.Call(daemon.DebugPath, req[0], &res)
	require.NoError(tb.t, err)
	require.Equal(tb.t, code, http.StatusOK)
	return &res
}

func (tb *testBase) prefetch(sph, path string) {
	sock := filepath.Join(tb.cachedir, "styx.sock")
	c := client.NewClient(sock)
	var res daemon.Status
	req := &daemon.PrefetchReq{Path: path, StorePath: sph}
	code, err := c.Call(daemon.PrefetchPath, req, &res)
	require.NoError(tb.t, err)
	require.Equal(tb.t, code, http.StatusOK)
	require.True(tb.t, res.Success, "error:", res.Error)
}

func (tb *testBase) dropCaches() {
	fd, err := unix.Open("/proc/sys/vm/drop_caches", unix.O_WRONLY, 0)
	require.NoError(tb.t, err)
	unix.Write(fd, []byte("3"))
	unix.Close(fd)
}

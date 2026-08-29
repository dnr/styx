package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/pb"
	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

// socket server + mount management

// Does a transaction on a record in imageBucket. f should mutate its argument and return nil.
// If f returns an error, the record will not be written.
func (s *Server) imageTx(sph string, f func(*pb.DbImage) error) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		var img pb.DbImage
		b := tx.Bucket(imageBucket)
		if buf := b.Get([]byte(sph)); buf != nil {
			if err := proto.Unmarshal(buf, &img); err != nil {
				return err
			}
		}
		if err := f(&img); err != nil {
			return err
		} else if buf, err := proto.Marshal(&img); err != nil {
			return err
		} else {
			return b.Put([]byte(sph), buf)
		}
	})
}

func (s *Server) startSocketServer() error {
	mux := http.NewServeMux()
	mux.HandleFunc(InitPath, jsonmw(s.handleInitReq))
	mux.HandleFunc(MountPath, jsonmw(s.handleMountReq))
	mux.HandleFunc(UmountPath, jsonmw(s.handleUmountReq))
	mux.HandleFunc(MaterializePath, jsonmw(s.handleMaterializeReq))
	mux.HandleFunc(VaporizePath, jsonmw(s.handleVaporizeReq))
	mux.HandleFunc(PrefetchPath, jsonmw(s.handlePrefetchReq))
	mux.HandleFunc(TarballPath, jsonmw(s.handleTarballReq))
	mux.HandleFunc(GcPath, jsonmw(s.handleGcReq))
	mux.HandleFunc(DebugPath, jsonmw(s.handleDebugReq))
	mux.HandleFunc(RepairPath, jsonmw(s.handleRepairReq))
	mux.HandleFunc("/pprof/", pprof.Index)
	mux.HandleFunc("/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/pprof/profile", pprof.Profile)
	mux.HandleFunc("/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/pprof/trace", pprof.Trace)
	err := s.runSocketServer(filepath.Join(s.cfg.CachePath, Socket), mux)
	if err != nil {
		return err
	}

	if s.cfg.PublicSock != "" {
		mux := http.NewServeMux()
		mux.HandleFunc(TarballPath, jsonmw(s.handleTarballReq))
		mux.HandleFunc(DebugPath, jsonmw(s.handleDebugReq))
		err := s.runSocketServer(s.cfg.PublicSock, mux)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) runSocketServer(socketPath string, mux http.Handler) error {
	os.Remove(socketPath)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socketPath})
	if err != nil {
		return fmt.Errorf("failed to listen on unix socket %s: %w", socketPath, err)
	}
	_ = os.Chmod(socketPath, 0o777)
	s.shutdownWait.Add(1)
	go func() {
		defer s.shutdownWait.Done()
		srv := &http.Server{Handler: mux}
		go srv.Serve(l)
		<-s.shutdownChan
		log.Printf("stopping http server")
		srv.Close()
	}()
	return nil
}

type errWithStatus struct {
	error
	status int
}

func mwErr(status int, format string, a ...any) error {
	return &errWithStatus{
		error:  fmt.Errorf(format, a...),
		status: status,
	}
}

func mwErrE(status int, e error) error {
	return &errWithStatus{
		error:  e,
		status: status,
	}
}

func jsonmw[reqT, resT any](f func(context.Context, *reqT) (*resT, error)) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if r := recover(); r != nil {
				log.Println("http handler panic", r)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()

		w.Header().Set(common.CTHdr, common.CTJson)
		wEnc := json.NewEncoder(w)

		var req reqT
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			wEnc.Encode(nil)
			return
		}

		parts := make([]any, 0, 7)
		parts = append(parts, r.URL.Path)

		if encReq, err := json.Marshal(req); err == nil {
			parts = append(parts, string(encReq))
		}

		res, err := f(r.Context(), &req)

		if err == nil {
			w.WriteHeader(http.StatusOK)
			if res != nil {
				wEnc.Encode(res)
			} else {
				wEnc.Encode(&Status{Success: true})
			}
			parts = append(parts, " -> ", "OK")
			log.Print(parts...)
			return
		}

		status := http.StatusInternalServerError
		if ewc, ok := err.(*errWithStatus); ok {
			status = ewc.status
		}

		w.WriteHeader(status)
		if res != nil {
			wEnc.Encode(res)
		} else {
			wEnc.Encode(&Status{Success: false, Error: err.Error()})
		}
		parts = append(parts, " -> ", err.Error())
		log.Print(parts...)
	}
}

func (s *Server) handleInitReq(ctx context.Context, r *InitReq) (*Status, error) {
	if s.p() != nil {
		// TODO: add ability to modify some params
		return nil, mwErr(http.StatusConflict, "already initialized")
	} else if err := verifyParams(r.Params.GetParams()); err != nil {
		return nil, mwErrE(http.StatusBadRequest, err)
	} else if len(r.PubKeys) == 0 {
		return nil, mwErr(http.StatusBadRequest, "missing public keys")
	} else if r.Params.ManifesterUrl == "" {
		return nil, mwErr(http.StatusBadRequest, "missing manifester url")
	} else if r.Params.ManifestCacheUrl == "" {
		return nil, mwErr(http.StatusBadRequest, "missing manifest cache url")
	} else if r.Params.ChunkReadUrl == "" {
		return nil, mwErr(http.StatusBadRequest, "missing chunk read url")
	} else if r.Params.ChunkDiffUrl == "" {
		return nil, mwErr(http.StatusBadRequest, "missing chunk diff url")
	} else if keys, err := common.LoadPubKeys(r.PubKeys); err != nil {
		return nil, mwErrE(http.StatusBadRequest, err)
	} else if err = s.postInit(&r.Params, keys); err != nil {
		return nil, err
	}
	return nil, s.db.Update(func(tx *bbolt.Tx) error {
		mb := tx.Bucket(metaBucket)
		if mb.Get(metaParams) != nil {
			// shouldn't happen here since postInit does CAS
			return errors.New("conflict on meta params update")
		}
		dp := pb.DbParams{
			Params: &r.Params,
			Pubkey: r.PubKeys,
		}
		if b, err := proto.Marshal(&dp); err != nil {
			return err
		} else {
			return mb.Put(metaParams, b)
		}
	})
}

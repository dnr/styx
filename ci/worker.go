package ci

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	ssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"golang.org/x/exp/slices"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/dnr/styx/common"
)

type (
	WorkerConfig struct {
		TemporalSSM string
		HostPort    string
		Namespace   string
		ApiKey      string

		RunWorker      bool
		RunScaler      bool
		RunHeavyWorker bool

		ScaleInterval time.Duration
		AsgGroupName  string
	}

	activities struct {
		cfg WorkerConfig
	}

	heavyActivities struct {
		cfg WorkerConfig
	}

	scaler struct {
		cfg      WorkerConfig
		c        client.Client
		notifyCh chan struct{}
		prev     int
		asgcli   *autoscaling.Client
	}

	pathInfoJson struct {
		Path       string   `json:"path"`
		NarSize    int64    `json:"narSize"`
		Signatures []string `json:"signatures"`
	}
)

const (
	// poll
	pollInterval   = 5 * time.Minute
	heartbeatExtra = 15 * time.Second

	// build
	buildHeartbeat = 1 * time.Minute
	buildTimeout   = 2 * time.Hour
)

var globalScaler atomic.Pointer[scaler]

func RunWorker(ctx context.Context, cfg WorkerConfig) error {
	if cfg.RunWorker && cfg.RunHeavyWorker {
		return errors.New("can't run both worker and heavy worker")
	} else if !cfg.RunWorker && !cfg.RunHeavyWorker {
		return errors.New("must run either worker or heavy worker")
	}

	if cfg.TemporalSSM != "" {
		params, err := getStringFromSSM(cfg.TemporalSSM)
		if err != nil {
			return err
		}
		parts := strings.SplitN(params, "~", 3)
		if len(parts) < 3 {
			return errors.New("bad params format")
		}
		cfg.HostPort, cfg.Namespace, cfg.ApiKey = parts[0], parts[1], parts[2]
	}

	co := client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
	}
	if cfg.ApiKey != "" {
		co.Credentials = client.NewAPIKeyStaticCredentials(cfg.ApiKey)
		// TODO: remove after go sdk does this automatically
		co.ConnectionOptions = client.ConnectionOptions{
			TLS: &tls.Config{},
			DialOptions: []grpc.DialOption{
				grpc.WithUnaryInterceptor(
					func(ctx context.Context, method string, req any, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
						return invoker(
							metadata.AppendToOutgoingContext(ctx, "temporal-namespace", cfg.Namespace),
							method,
							req,
							reply,
							cc,
							opts...,
						)
					},
				),
			},
		}
	}
	c, err := client.DialContext(ctx, co)
	if err != nil {
		return err
	}

	var w worker.Worker
	if cfg.RunWorker {
		w := worker.New(c, taskQueue, worker.Options{})
		w.RegisterWorkflow(ci)
		w.RegisterActivity(&activities{cfg: cfg})

		if cfg.RunScaler {
			s, err := newScaler(cfg, c)
			if err != nil {
				return err
			}
			globalScaler.Store(s)
			go s.run()
		}
	} else if cfg.RunHeavyWorker {
		w := worker.New(c, heavyTaskQueue, worker.Options{})
		w.RegisterActivity(&heavyActivities{cfg: cfg})
	}
	return w.Run(worker.InterruptCh())
}

// main workflow
func ci(ctx workflow.Context, args CiArgs) error {
	l := workflow.GetLogger(ctx)
	for !workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
		// poll nixos channels
		// TODO: check for bump on styx "release" branch also
		pres, err := ciPoll(ctx, &pollReq{
			Channel:   args.Channel,
			LastRelID: args.LastRelID,
		})
		if err != nil {
			// only non-retryable errors end up here
			l.Error("poll error", "error", err)
			workflow.Sleep(ctx, time.Hour)
			continue
		}
		l.Info("poll got new relid", pres.RelID)
		args.LastRelID = pres.RelID

		// build
		bres, err := ciBuild(ctx, &buildReq{
			Channel:   args.Channel,
			RelID:     pres.RelID,
			ConfigURL: args.ConfigURL,
		})
		if err != nil {
			l.Error("build error", "error", err)
			workflow.Sleep(ctx, time.Hour)
			continue
		}
		l.Info("build success at", pres.RelID)

		// manifest
		err = ciManifest(ctx, bres.StorePaths)
		if err != nil {
			l.Error("manifest error error", "error", err)
			workflow.Sleep(ctx, time.Hour)
			continue
		}
		l.Info("manifested all paths")
	}
	return workflow.NewContinueAsNewError(ctx, ci, args)
}

func ciPoll(ctx workflow.Context, req *pollReq) (*pollRes, error) {
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		HeartbeatTimeout:    pollInterval + heartbeatExtra,
		StartToCloseTimeout: 365 * 24 * time.Hour,
	})
	var res pollRes
	var a *activities
	return &res, workflow.ExecuteActivity(actx, a.poll, req).Get(ctx, &res)
}

func ciBuild(ctx workflow.Context, req *buildReq) (*buildRes, error) {
	pokeScaler(ctx)
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           heavyTaskQueue,
		HeartbeatTimeout:    buildHeartbeat,
		StartToCloseTimeout: buildTimeout,
	})
	var res buildRes
	var a *heavyActivities
	return &res, workflow.ExecuteActivity(actx, a.heavyBuild, req).Get(ctx, &res)
}

func ciManifest(ctx workflow.Context, storePaths []pathAndSize) error {
	const batchSize = 100
	l := workflow.GetLogger(ctx)

	sp := slices.Clone(storePaths)
	rand.Shuffle(len(sp), func(i, j int) { sp[i], sp[j] = sp[j], sp[i] })
	var futs []workflow.Future
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		// TODO: maybe nice to use heartbeats?
		StartToCloseTimeout: 1 * time.Minute * batchSize,
	})
	var a *activities
	for _, batch := range batches(sp, batchSize) {
		req := &manifestReq{StorePaths: batch}
		futs = append(futs, workflow.ExecuteActivity(actx, a.manifest, req))
	}
	var err error
	for _, f := range futs {
		if e := f.Get(ctx, nil); e != nil {
			l.Error("manifest error", "error", e)
			err = common.Or(err, e)
		}
	}
	return err
}

func pokeScaler(ctx workflow.Context) {
	if !workflow.IsReplaying(ctx) {
		if s := globalScaler.Load(); s != nil {
			s.poke()
		}
	}
}

// autoscaler

func newScaler(cfg WorkerConfig, c client.Client) (*scaler, error) {
	awscfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}
	return &scaler{
		cfg:      cfg,
		c:        c,
		notifyCh: make(chan struct{}, 1),
		prev:     -1,
		asgcli:   autoscaling.NewFromConfig(awscfg),
	}, nil
}

func (s *scaler) run() {
	t := time.NewTicker(s.cfg.ScaleInterval).C
	for {
		select {
		case <-t:
		case <-s.notifyCh:
			// wait a bit for activity info to be updated
			time.Sleep(5 * time.Second)
		}
		s.iter()
	}
}

func (s *scaler) iter() {
	pending, err := s.getPending()
	if err != nil {
		log.Println("scaler getPending error:", err)
		return
	}

	target := 0
	if pending > 0 {
		target = 1
	}

	if target != s.prev {
		s.setSize(target)
		s.prev = target
	}
}

func (s *scaler) getPending() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := s.c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: s.cfg.Namespace,
		Query:     fmt.Sprintf(`WorkflowType = "%s" and ExecutionStatus = "Running"`, workflowType),
	})
	if err != nil {
		return 0, err
	}

	total := 0
	for _, ex := range res.Executions {
		desc, err := s.c.DescribeWorkflowExecution(ctx, ex.Execution.WorkflowId, ex.Execution.RunId)
		if err != nil {
			return 0, err
		}
		for _, act := range desc.PendingActivities {
			if strings.Contains(strings.ToLower(act.ActivityType.Name), "heavy") {
				total++
			}
		}
	}
	return total, nil
}

func (s *scaler) setSize(size int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := s.asgcli.SetDesiredCapacity(ctx, &autoscaling.SetDesiredCapacityInput{
		AutoScalingGroupName: &s.cfg.AsgGroupName,
		DesiredCapacity:      aws.Int32(int32(size)),
	})
	if err == nil {
		log.Println("set asg capacity to", size)
	} else {
		log.Println("asg set capacity error:", err)
	}
}

func (s *scaler) poke() { s.notifyCh <- struct{}{} }

// activities

func (a *activities) poll(ctx context.Context, req *pollReq) (*pollRes, error) {
	interval := activity.GetInfo(ctx).HeartbeatTimeout - heartbeatExtra
	t := time.NewTicker(interval)
	defer t.Stop()

	for !(<-t.C).IsZero() && ctx.Err() == nil {
		relid, err := getRelID(ctx, req.Channel)
		if err != nil {
			return nil, err
		}
		if getRelNum(relid) > getRelNum(req.LastRelID) {
			return &pollRes{RelID: relid}, nil
		}
		activity.RecordHeartbeat(ctx)
	}
	return nil, ctx.Err()
}

func (a *activities) manifest(ctx context.Context, req *manifestReq) (*manifestRes, error) {
	// FIXME
	return nil, nil
}

func getRelID(ctx context.Context, channel string) (string, error) {
	u := "https://channels.nixos.org/" + channel
	hreq, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return "", err
	}
	hres, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return "", err
	}
	io.Copy(io.Discard, hres.Body)
	hres.Body.Close()
	// will redirect to a url like:
	// https://releases.nixos.org/nixos/23.11/nixos-23.11.7609.5c2ec3a5c2ee
	// take last part as relid
	return path.Base(hres.Request.URL.Path), nil
}

func getRelNum(relid string) int {
	// e.g. "nixos-23.11.7609.5c2ec3a5c2ee"
	if parts := strings.Split(relid, "."); len(parts) > 2 {
		if i, err := strconv.Atoi(parts[len(parts)-2]); err == nil {
			return i
		}
	}
	return 0
}

// heavy activities (all must have "heavy" in the name for scaler to work)

func (a *heavyActivities) heavyBuild(ctx context.Context, req *buildReq) (*buildRes, error) {
	l := activity.GetLogger(ctx)
	info := activity.GetInfo(ctx)

	var stage atomic.Value
	stage.Store("init")

	go func() {
		for ctx.Err() == nil {
			time.Sleep(5 * time.Second)
			activity.RecordHeartbeat(ctx, stage.Load())
		}
	}()

	// build

	l.Info("building nixos...")
	stage.Store("build")
	cmd := exec.CommandContext(ctx,
		"nix-build",
		"<nixpkgs/nixos>",
		"-A", "system",
		"--no-out-link",
		"--timeout", strconv.Itoa(int(time.Until(info.Deadline).Seconds())),
		"--keep-going",
		"-j", "auto",
		"-I", "nixpkgs="+makeNixexprsUrl(req.Channel, req.RelID),
		"-I", "nixos-config="+req.ConfigURL,
	)
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "NIX_PATH=")
	out, err := cmd.Output()
	if err != nil {
		l.Error("build error", "error", err)
		return nil, err
	}

	// list

	l.Info("getting package list from closure...")
	stage.Store("list")
	cmd = exec.CommandContext(ctx,
		"nix", "--extra-experimental-features", "nix-command",
		"path-info",
		"--json",
		"--recursive",
		string(out),
	)
	cmd.Stderr = os.Stderr
	j, err := cmd.Output()
	if err != nil {
		l.Error("get closure error", "error", err)
		return nil, err
	}
	var pathInfo []pathInfoJson
	var toSign bytes.Buffer
	var toCopy bytes.Buffer
	if err := json.Unmarshal(j, &pathInfo); err != nil {
		l.Error("get closure json unmarshal error", "error", err)
		return nil, err
	}

	bres := buildRes{
		StorePaths: make([]pathAndSize, 0, len(pathInfo)),
	}

	for _, pi := range pathInfo {
		// filter out tiny build-specific stuff
		if strings.Contains(pi.Path, "-nixos-system-") ||
			strings.Contains(pi.Path, "-security-wrapper-") ||
			strings.Contains(pi.Path, "-unit-") ||
			strings.Contains(pi.Path, "-etc-") {
			continue
		}
		// only sign if not from public cache
		if !pi.fromPublicCache() {
			toSign.WriteString(pi.Path + "\n")
		}
		// copy is going to take closures anyway so just pass all to copy
		toCopy.WriteString(pi.Path + "\n")
		bres.StorePaths = append(bres.StorePaths, pathAndSize{
			Path: pi.Path,
			Size: pi.NarSize, // size is used as hint to manifest client
		})
	}

	// sign

	if req.SignKeySSM != "" {
		l.Info("signing packages...")
		stage.Store("sign")
		keyfile, err := getFileFromSSM(req.SignKeySSM)
		if err != nil {
			return nil, err
		}
		defer os.Remove(keyfile)
		cmd = exec.CommandContext(ctx, "nix", "store", "sign", "--key-file", keyfile, "--stdin")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = &toSign
		err = cmd.Run()
		if err != nil {
			l.Error("sign error", "error", err)
			return nil, err
		}
	}

	// copy

	if req.CopyDest != "" {
		l.Info("copying packages to dest store...")
		stage.Store("copy")
		cmd = exec.CommandContext(ctx,
			"nix", "--extra-experimental-features", "nix-command",
			"copy",
			"--to", req.CopyDest,
			"--stdin",
			"--verbose",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = &toCopy
		err = cmd.Run()
		if err != nil {
			l.Error("copy error", "error", err)
			return nil, err
		}
	}

	return &bres, nil
}

func makeNixexprsUrl(channel, relid string) string {
	// turn nixos-23.11, nixos-23.11.7609.5c2ec3a5c2ee into
	// https://releases.nixos.org/nixos/23.11/nixos-23.11.7609.5c2ec3a5c2ee/nixexprs.tar.xz
	return "https://releases.nixos.org/" + strings.ReplaceAll(channel, "-", "/") + "/" + relid + "/nixexprs.tar.xz"
}

func getStringFromSSM(name string) (string, error) {
	ssmcli, err := getSSMCli()
	if err != nil {
		return "", err
	}
	decrypt := true
	out, err := ssmcli.GetParameter(context.Background(), &ssm.GetParameterInput{
		Name:           &name,
		WithDecryption: &decrypt,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(*out.Parameter.Value), nil
}

func getFileFromSSM(name string) (string, error) {
	val, err := getStringFromSSM(name)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "ssmtmp")
	if err != nil {
		return "", err
	}
	defer f.Close()
	f.WriteString(val)
	return f.Name(), nil
}

var getAwsCfg = sync.OnceValues(func() (aws.Config, error) {
	return awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithEC2IMDSRegion())
})

var getSSMCli = sync.OnceValues(func() (*ssm.Client, error) {
	awscfg, err := getAwsCfg()
	if err != nil {
		return nil, err
	}
	return ssm.NewFromConfig(awscfg), nil
})

func (pi *pathInfoJson) fromPublicCache() bool {
	for _, s := range pi.Signatures {
		if strings.HasPrefix(s, "cache.nixos.org-1:") {
			return true
		}
	}
	return false
}

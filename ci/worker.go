package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/storepath"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

type (
	WorkerConfig struct {
		TemporalParams string
		SmtpParams     string

		RunWorker      bool
		RunScaler      bool
		RunHeavyWorker bool

		ScaleInterval time.Duration
		AsgGroupName  string

		CacheSignKeySSM string

		ManifestPubKeys    []string
		ManifestSignKeySSM []string

		CSWCfg manifester.ChunkStoreWriteConfig
		MBCfg  manifester.ManifestBuilderConfig
	}

	activities struct {
		cfg WorkerConfig
	}

	heavyActivities struct {
		cfg   WorkerConfig
		b     *manifester.ManifestBuilder
		zp    *common.ZstdCtxPool
		s3cli *s3.Client
	}

	scaler struct {
		cfg       WorkerConfig
		ns        string
		c         client.Client
		notifyCh  chan struct{}
		prev      int
		asgcli    *autoscaling.Client
		startTime time.Time // non-zero when we scale up asg
	}

	manifestReq struct {
		upstream  string
		storePath string
	}
)

const (
	// poll
	releasePollInterval = 10 * time.Minute
	repoPollInterval    = 10 * time.Minute // github unauthenticated limit = 60/hour/ip

	// build
	buildHeartbeat    = 1 * time.Minute
	buildTimeout      = 2 * time.Hour
	buildStartTimeout = 30 * time.Minute // if build hasn't started yet, abort

	// gc
	gcInterval = 7 * 24 * time.Hour
	gcMaxAge   = 210 * 24 * time.Hour
)

var globalScaler atomic.Pointer[scaler]

func RunWorker(ctx context.Context, cfg WorkerConfig) error {
	if cfg.RunWorker && cfg.RunHeavyWorker {
		return errors.New("can't run both worker and heavy worker")
	} else if !cfg.RunWorker && !cfg.RunHeavyWorker {
		return errors.New("must run either worker or heavy worker")
	}

	c, namespace, err := getTemporalClient(ctx, cfg.TemporalParams)
	if err != nil {
		return err
	}

	var w worker.Worker
	if cfg.RunWorker {
		w = worker.New(c, taskQueue, worker.Options{})
		w.RegisterWorkflow(ci)
		w.RegisterActivity(&activities{cfg: cfg})

		if cfg.RunScaler {
			s, err := newScaler(cfg, namespace, c)
			if err != nil {
				return err
			}
			globalScaler.Store(s)
			go s.run()
		}
	} else if cfg.RunHeavyWorker {
		cs, err := manifester.NewChunkStoreWrite(cfg.CSWCfg)
		if err != nil {
			return err
		}
		if cfg.MBCfg.PublicKeys, err = common.LoadPubKeys(cfg.ManifestPubKeys); err != nil {
			return err
		}
		if cfg.MBCfg.SigningKeys, err = getParamsAsKeys(cfg.ManifestSignKeySSM); err != nil {
			return err
		}
		b, err := manifester.NewManifestBuilder(cfg.MBCfg, cs)
		if err != nil {
			return err
		}
		w = worker.New(c, heavyTaskQueue, worker.Options{})
		s3cli, err := getS3Cli()
		if err != nil {
			return err
		}
		ha := &heavyActivities{
			cfg:   cfg,
			b:     b,
			zp:    common.GetZstdCtxPool(),
			s3cli: s3cli,
		}
		w.RegisterActivity(ha)
	}
	return w.Run(worker.InterruptCh())
}

// main workflow
func ci(ctx workflow.Context, args *CiArgs) error {
	var a *activities
	l := workflow.GetLogger(ctx)
	forceCh := workflow.GetSignalChannel(ctx, "buildnow")
	for !workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
		// poll nixos channels
		cctx, cancel := workflow.WithCancel(ctx)
		actx := withPollActivity(cctx, releasePollInterval, time.Minute)
		chanF := workflow.ExecuteActivity(actx, a.PollChannel, &pollChannelReq{
			Channel:   args.Channel,
			LastRelID: args.LastRelID,
		})

		// poll github repo
		actx = withPollActivity(cctx, repoPollInterval, time.Minute)
		styxRepoF := workflow.ExecuteActivity(actx, a.PollRepo, &pollRepoReq{
			Config:     args.StyxRepo,
			LastCommit: args.LastStyxCommit,
		})

		var err1, err2 error
		sel := workflow.NewSelector(ctx)
		sel.
			AddFuture(chanF, func(f workflow.Future) {
				var res pollChannelRes
				if err1 = f.Get(ctx, &res); err1 == nil {
					l.Info("poll got new nix channel release", "relid", res.RelID)
					args.LastRelID = res.RelID
				}
			}).
			AddFuture(styxRepoF, func(f workflow.Future) {
				var res pollRepoRes
				if err2 = f.Get(ctx, &res); err2 == nil {
					l.Info("poll got new styx commit", "commit", res.Commit)
					args.LastStyxCommit = res.Commit
				}
			}).
			AddReceive(forceCh, func(workflow.ReceiveChannel, bool) {
				forceCh.ReceiveAsync(nil)
			}).
			Select(ctx)

		for sel.HasPending() {
			sel.Select(ctx)
		}

		cancel() // cancel other poller

		if err1 != nil || err2 != nil {
			// only non-retryable errors end up here
			l.Error("poll error", "err1", err1, "err2", err2)
			workflow.Sleep(ctx, time.Hour)
			continue
		}

		// build
		if args.LastRelID == "" || args.LastStyxCommit == "" {
			continue
		}

		buildStart := workflow.Now(ctx)
		l.Info("building", "relid", args.LastRelID, "styx", args.LastStyxCommit)
		bres, err := ciBuild(ctx, &buildReq{
			Args:       args,
			RelID:      args.LastRelID,
			StyxCommit: args.LastStyxCommit,
		})
		if err != nil {
			l.Error("build error", "error", err)
			var deets *buildErrDetails
			if appErr, ok := err.(*temporal.ApplicationError); ok {
				_ = appErr.Details(&deets)
			}
			ciNotify(ctx, &notifyReq{
				Args:         args,
				Error:        err.Error(),
				ErrorDetails: deets,
			})
			workflow.Sleep(ctx, time.Minute)
			continue
		} else if bres.FakeError != "" {
			l.Error("build fake error", "error", bres.FakeError)
			continue
		}
		l.Info("build succeeded", "relid", args.LastRelID, "styx", args.LastStyxCommit)
		prevNames := args.PrevNames
		args.PrevNames = bres.Names
		if bres.NewLastGC > 0 {
			args.LastGC = bres.NewLastGC
		}

		// notify
		ciNotify(ctx, &notifyReq{
			Args:          args,
			RelID:         args.LastRelID,
			StyxCommit:    args.LastStyxCommit,
			BuildElapsed:  workflow.Now(ctx).Sub(buildStart).Round(time.Second),
			PrevNames:     prevNames,
			NewNames:      bres.Names,
			ManifestStats: bres.ManifestStats,
			GCSummary:     bres.GCSummary,
		})
	}
	return workflow.NewContinueAsNewError(ctx, ci, args)
}

func withPollActivity(ctx workflow.Context, interval, s2ct time.Duration) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: 365 * 24 * time.Hour,
		StartToCloseTimeout:    s2ct,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    interval,
			BackoffCoefficient: 1.0,
		},
	})
}

func ciBuild(ctx workflow.Context, req *buildReq) (*buildRes, error) {
	pokeScaler(ctx)
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           heavyTaskQueue,
		HeartbeatTimeout:    buildHeartbeat,
		StartToCloseTimeout: buildTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			// this activity is expensive, don't let it fail forever
			MaximumAttempts: 1,
		},
	})
	var res buildRes
	var a *heavyActivities
	return &res, workflow.ExecuteActivity(actx, a.HeavyBuild, req).Get(ctx, &res)
}

func ciNotify(ctx workflow.Context, req *notifyReq) error {
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
	})
	var a *activities
	return workflow.ExecuteActivity(actx, a.Notify, req).Get(ctx, nil)
}

func pokeScaler(ctx workflow.Context) {
	if !workflow.IsReplaying(ctx) {
		if s := globalScaler.Load(); s != nil {
			s.poke()
		}
	}
}

// autoscaler

func newScaler(cfg WorkerConfig, ns string, c client.Client) (*scaler, error) {
	awscfg, err := getAwsCfg()
	if err != nil {
		return nil, err
	}
	return &scaler{
		cfg:      cfg,
		ns:       ns,
		c:        c,
		notifyCh: make(chan struct{}, 1),
		prev:     -1,
		asgcli:   autoscaling.NewFromConfig(awscfg),
	}, nil
}

func (s *scaler) run() {
	t := time.NewTicker(s.cfg.ScaleInterval).C
	for {
		s.iter()
		select {
		case <-t:
		case <-s.notifyCh:
			// wait a bit for activity info to be updated
			time.Sleep(5 * time.Second)
		}
	}
}

func (s *scaler) iter() {
	scheduled, started, err := s.getPending()
	if err != nil {
		log.Println("scaler getPending error:", err)
		return
	}

	target := 0
	if s.startTime.IsZero() {
		if scheduled > 0 || started > 0 {
			target = 1
			s.startTime = time.Now()
		}
	} else {
		if scheduled > 0 || started > 0 {
			target = 1
			if started == 0 && time.Since(s.startTime) > buildStartTimeout {
				log.Printf("heavy worker did not pick up task in %v, aborting", buildStartTimeout)
				target = 0
			}
		}
		if target == 0 {
			s.startTime = time.Time{}
		}
	}

	if target != s.prev {
		s.setSize(target)
		s.prev = target
	}
}

func (s *scaler) getPending() (int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := s.c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: s.ns,
		Query:     fmt.Sprintf(`WorkflowType = "%s" and ExecutionStatus = "Running"`, workflowType),
	})
	if err != nil {
		return 0, 0, err
	}

	var scheduled, started int
	for _, ex := range res.Executions {
		desc, err := s.c.DescribeWorkflowExecution(ctx, ex.Execution.WorkflowId, ex.Execution.RunId)
		if err != nil {
			return 0, 0, err
		}
		for _, act := range desc.PendingActivities {
			if strings.Contains(strings.ToLower(act.ActivityType.Name), "heavy") {
				switch act.State {
				case enumspb.PENDING_ACTIVITY_STATE_SCHEDULED:
					scheduled++
				case enumspb.PENDING_ACTIVITY_STATE_STARTED:
					started++
				}
			}
		}
	}
	return scheduled, started, nil
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

func (a *activities) PollChannel(ctx context.Context, req *pollChannelReq) (*pollChannelRes, error) {
	relid, err := getRelID(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	if getRelNum(relid) <= getRelNum(req.LastRelID) {
		return nil, temporal.NewApplicationError("same as before", "retry", relid)
	}
	return &pollChannelRes{RelID: relid}, nil
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

type ghLatestCommit struct {
	Oid  string `json:"oid"`
	Date string `json:"date"`
}

func (a *activities) PollRepo(ctx context.Context, req *pollRepoReq) (*pollRepoRes, error) {
	res, err := getLatestCommit(ctx, req.Config.Repo, req.Config.Branch)
	if err != nil {
		return nil, err
	}
	if res.Oid == req.LastCommit {
		return nil, temporal.NewApplicationError("same as before", "retry", res)
	}
	return &pollRepoRes{Commit: res.Oid}, nil
}

func getLatestCommit(ctx context.Context, repo, branch string) (*ghLatestCommit, error) {
	u, err := url.Parse(repo)
	if err != nil {
		return nil, err
	}
	uStr := u.JoinPath("latest-commit", branch).String()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, uStr, nil)
	if err != nil {
		return nil, err
	}
	hreq.Header.Add("Accept", "application/json")
	hres, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(hres.Body)
	hres.Body.Close()
	if err != nil {
		return nil, err
	}
	var res ghLatestCommit
	return common.ValOrErr(&res, json.Unmarshal(body, &res))
}

// heavy activities (all must have "heavy" in the name for scaler to work)

func (a *heavyActivities) HeavyBuild(ctx context.Context, req *buildReq) (retBuildRes *buildRes, retErr error) {
	l := activity.GetLogger(ctx)
	info := activity.GetInfo(ctx)

	defer func() {
		if retErr == nil {
			return
		}
		// grab some logs
		startLog, _ := exec.Command("journalctl", "-u", "charon", "-n", "+10").Output()
		endLog, _ := exec.Command("journalctl", "-u", "charon", "-n", "100").Output()
		logs := string(startLog) + "\n...\n" + string(endLog)
		details := &buildErrDetails{Logs: logs}
		errType := fmt.Sprintf("%T", retErr)
		retErr = temporal.NewApplicationError(retErr.Error(), errType, details)
	}()

	var stageName atomic.Value
	stage := func(s string) {
		l.Info("====================== STAGE " + s)
		stageName.Store(s)
	}
	stage("INIT")

	go func() {
		for ctx.Err() == nil {
			time.Sleep(5 * time.Second)
			activity.RecordHeartbeat(ctx, stageName.Load())
		}
	}()

	// fetch nixexprs

	l.Info("fetching nixexprs...")
	stage("NIXEXPRS")
	nixexprs := makeNixexprsUrl(req.Args.Channel, req.RelID)
	// even though we will eventually manifest this tarball, we don't need --name here because
	// it doesn't affect the contents, only the store path.
	cmd := exec.CommandContext(ctx, common.NixBin+"-prefetch-url", "--unpack", "--print-path", nixexprs)
	out, err := cmd.Output()
	if err != nil {
		l.Error("fetch error", "error", err)
		return nil, err
	}
	var nixexprsLocalPath string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, storepath.StoreDir) {
			nixexprsLocalPath = line
		}
	}
	if nixexprsLocalPath == "" {
		l.Error("fetch error", "error", "missing local path")
		return nil, fmt.Errorf("nix-prefetch-url output missing path")
	}

	// build

	l.Info("building nixos...")
	stage("BUILD")
	// Note: global nix config has our nix cache as substituter so we can pull stuff back
	// from a previous run
	cmd = exec.CommandContext(ctx,
		common.NixBin+"-build",
		"-E", "(import <nixpkgs/nixos> { configuration = <styx/ci/config>; }).system",
		"--no-out-link",
		"--timeout", strconv.Itoa(int(time.Until(info.Deadline).Seconds())),
		"--keep-going",
		"-I", "nixpkgs="+nixexprsLocalPath,
		"-I", "styx="+makeGithubUrl(req.Args.StyxRepo, req.StyxCommit),
	)
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "NIX_PATH=")
	out, err = cmd.Output()
	if err != nil {
		l.Error("build error", "error", err)
		return nil, err
	}

	// list

	l.Info("getting package list from closure...")
	stage("LIST")
	cmd = exec.CommandContext(ctx,
		common.NixBin, "--extra-experimental-features", "nix-command",
		"path-info",
		"--json",
		"--recursive",
		strings.TrimSpace(string(out)),
	)
	cmd.Stderr = os.Stderr
	j, err := cmd.Output()
	if err != nil {
		l.Error("get closure error", "error", err)
		return nil, err
	}
	var pathInfo map[string]struct {
		NarSize    int64    `json:"narSize"`
		Signatures []string `json:"signatures"`
	}
	if err := json.Unmarshal(j, &pathInfo); err != nil {
		l.Error("get closure json unmarshal error", "error", err)
		return nil, err
	}

	var toSign, toCopy bytes.Buffer
	toManifest := make([]manifestReq, 0, len(pathInfo))
	names := make([]string, 0, len(pathInfo))
	sphForRoot := make([]string, 0, len(pathInfo)+1)

	for piPath, pi := range pathInfo {
		// add all to root record in case some of these filtered ones end up getting copied
		sphForRoot = append(sphForRoot, piPath[11:43])

		// filter out tiny build-specific stuff
		if strings.Contains(piPath, "-nixos-system-") ||
			strings.Contains(piPath, "-security-wrapper-") ||
			strings.Contains(piPath, "-unit-") ||
			strings.Contains(piPath, "-etc-") {
			continue
		}

		names = append(names, piPath[44:])
		public := fromPublicCache(pi.Signatures)
		upstream := req.Args.PublicCacheUpstream
		// only sign if not from public cache
		if !public {
			toSign.WriteString(piPath + "\n")
			upstream = req.Args.ManifestUpstream
		}
		// copy is going to take closures anyway so just pass all to copy
		toCopy.WriteString(piPath + "\n")
		// manifest everything
		toManifest = append(toManifest, manifestReq{
			upstream:  upstream,
			storePath: piPath,
		})
	}

	// sign

	if a.cfg.CacheSignKeySSM != "" {
		l.Info("signing packages...")
		stage("SIGN")
		keyfile, err := getParamsAsFile(a.cfg.CacheSignKeySSM)
		if err != nil {
			return nil, err
		}
		defer os.Remove(keyfile)
		cmd = exec.CommandContext(ctx,
			common.NixBin, "--extra-experimental-features", "nix-command",
			"store",
			"sign",
			"--key-file", keyfile,
			"--stdin",
		)
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

	if req.Args.CopyDest != "" {
		l.Info("copying packages to dest store...")
		stage("COPY")
		cmd = exec.CommandContext(ctx,
			common.NixBin, "--extra-experimental-features", "nix-command",
			"copy",
			"--to", req.Args.CopyDest,
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

	// manifest

	stage("MANIFEST")
	egCtx := errgroup.WithContext(ctx)
	egCtx.SetLimit(runtime.NumCPU())
	a.b.ClearStats()
	mcacheForRoot := make([]string, len(toManifest)+2)
	nixexprsI := len(toManifest)
	for i, m := range toManifest {
		m := m
		egCtx.Go(func() error {
			sph := m.storePath[11:43]
			mres, err := a.b.Build(egCtx, manifester.ModeNar, m.upstream, sph, 0, 0, m.storePath, false)
			if mres != nil {
				mcacheForRoot[i] = mres.CacheKey
			}
			return err
		})
	}
	// manifest nixexprs too
	egCtx.Go(func() error {
		mres, err := a.b.Build(egCtx, manifester.ModeGenericTarball, nixexprs, "", 0, 0, nixexprsLocalPath, false)
		if mres != nil {
			mcacheForRoot[nixexprsI] = mres.CacheKey
			mcacheForRoot[nixexprsI+1] = mres.EtagCacheKey
			// this is the only goroutine touching sphForRoot at this point so we can do this:
			sphForRoot = append(sphForRoot, mres.Sph)
		}
		return err
	})
	if err := egCtx.Wait(); err != nil {
		l.Error("manifest error", "error", err)
		return nil, err
	}

	// maybe missing EtagCacheKey
	for mcacheForRoot[len(mcacheForRoot)-1] == "" {
		mcacheForRoot = mcacheForRoot[:len(mcacheForRoot)-1]
	}

	// write root

	btime := time.Now()
	var gcSummary strings.Builder
	gc := gc{
		now:     btime,
		stage:   stage,
		summary: &gcSummary,
		zp:      a.zp,
		s3:      a.s3cli,
		bucket:  a.cfg.CSWCfg.ChunkBucket,
		age:     gcMaxAge,
	}

	stage("WRITE ROOT")
	root := &pb.BuildRoot{
		Meta: &pb.BuildRootMeta{
			BuildTime:  btime.Unix(),
			NixRelId:   req.RelID,
			StyxCommit: req.StyxCommit,
		},
		StorePathHash: sphForRoot,
		Manifest:      mcacheForRoot,
	}
	brkey := strings.Join([]string{
		"build",
		btime.Format(time.RFC3339),
		req.RelID,
		req.StyxCommit[:12],
	}, "@")
	err = gc.writeBuildRoot(ctx, root, brkey)
	if err != nil {
		l.Error("write build root error", "error", err)
		return nil, err
	}

	// gc

	newLastGC := req.Args.LastGC
	if btime.Unix()-req.Args.LastGC > int64(gcInterval.Seconds()) {
		newLastGC = btime.Unix()
		gc.run(ctx)
	}

	slices.Sort(names)
	names = slices.Compact(names)

	stage("DONE")
	return &buildRes{
		Names:         names,
		ManifestStats: a.b.Stats(),
		NewLastGC:     newLastGC,
		GCSummary:     gcSummary.String(),
	}, nil
}

func makeNixexprsUrl(channel, relid string) string {
	// turn "nixos-23.11", "nixos-23.11.7609.5c2ec3a5c2ee" into
	// "https://releases.nixos.org/nixos/23.11/nixos-23.11.7609.5c2ec3a5c2ee/nixexprs.tar.xz"
	return "https://releases.nixos.org/" + strings.ReplaceAll(channel, "-", "/") + "/" + relid + "/nixexprs.tar.xz"
}
func makeGithubUrl(repoConfig RepoConfig, commit string) string {
	// make url like: "https://github.com/dnr/styx/archive/7da079581765d13a37a2e0c27b4a461693384f20.tar.gz"
	return strings.TrimSuffix(repoConfig.Repo, "/") + "/archive/" + commit + ".tar.gz"
}

func getParams(src string) (string, error) {
	// try file
	v, err := os.ReadFile(src)
	if err == nil {
		return strings.TrimSpace(string(v)), nil
	} else if strings.Contains(src, "/") {
		return "", err
	}

	// does not exist as file and does not contain slash, try ssm
	ssmcli, err := getSSMCli()
	if err != nil {
		return "", err
	}
	decrypt := true
	out, err := ssmcli.GetParameter(context.Background(), &ssm.GetParameterInput{
		Name:           &src,
		WithDecryption: &decrypt,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(*out.Parameter.Value), nil
}

func getParamsAsFile(name string) (string, error) {
	val, err := getParams(name)
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

func getParamsAsKeys(names []string) ([]signature.SecretKey, error) {
	keys := make([]signature.SecretKey, len(names))
	for i, name := range names {
		if val, err := getParams(name); err != nil {
			return nil, err
		} else if keys[i], err = signature.LoadSecretKey(val); err != nil {
			return nil, err
		}
	}
	return keys, nil
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

var getS3Cli = sync.OnceValues(func() (*s3.Client, error) {
	awscfg, err := getAwsCfg()
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(awscfg), nil
})

func fromPublicCache(sigs []string) bool {
	for _, s := range sigs {
		if strings.HasPrefix(s, "cache.nixos.org-1:") {
			return true
		}
	}
	return false
}

package ci

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type (
	WorkerConfig struct {
		HostPort  string
		Namespace string
		ApiKey    string

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
)

var globalScaler atomic.Pointer[scaler]

func RunWorker(ctx context.Context, cfg WorkerConfig) error {
	if cfg.RunWorker && cfg.RunHeavyWorker {
		return errors.New("can't run both worker and heavy worker")
	} else if !cfg.RunWorker && !cfg.RunHeavyWorker {
		return errors.New("must run either worker or heavy worker")
	}

	co := client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
	}
	if cfg.ApiKey != "" {
		co.Credentials = client.NewAPIKeyStaticCredentials(cfg.ApiKey)
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
func ci(ctx workflow.Context) error {
	return nil
}

func pokeScaler(ctx workflow.Context) {
	workflow.SideEffect(ctx, func(workflow.Context) any {
		if s := globalScaler.Load(); s != nil {
			s.poke()
		}
		return nil
	})
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
			time.Sleep(3 * time.Second)
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
			if strings.Contains(act.ActivityType.Name, "Heavy") {
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

func (a *activities) DoSomething(ctx context.Context, args somethingArgs) error {
	return nil
}

// heavy activities (all must have "Heavy" in the name for scaler to work)

func (a *heavyActivities) HeavySomething(ctx context.Context, args somethingArgs) error {
	return nil
}

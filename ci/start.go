package ci

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/client"
)

type (
	StartConfig struct {
		TemporalParams string
		Args           CiArgs
	}
)

func Start(ctx context.Context, cfg StartConfig) error {
	c, _, err := getTemporalClient(ctx, cfg.TemporalParams)
	if err != nil {
		return err
	}

	opts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("ci-%s-%s", cfg.Args.StyxRepo.Branch, cfg.Args.Channel),
		TaskQueue: taskQueue,
	}
	run, err := c.ExecuteWorkflow(context.Background(), opts, ci, cfg.Args)
	if err != nil {
		return err
	}
	fmt.Println("workflow id:", run.GetID(), "run id:", run.GetRunID())
	return nil
}

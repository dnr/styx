package main

import (
	"log"

	"github.com/spf13/cobra"
)

type runE = func(*cobra.Command, []string) error

func chainRunE(prev, f runE) runE {
	if prev == nil {
		return f
	}
	return func(c *cobra.Command, args []string) error {
		if err := prev(c, args); err != nil {
			return err
		}
		return f(c, args)
	}
}

func cmd(c *cobra.Command, stuff ...any) *cobra.Command {
	for _, thing := range stuff {
		switch t := thing.(type) {
		case func(*cobra.Command):
			t(c)
		case *cobra.Command:
			c.AddCommand(t)
		case runE:
			c.RunE = chainRunE(c.RunE, t)
		case func(*cobra.Command) runE:
			c.RunE = chainRunE(c.RunE, t(c))
		default:
			log.Panicf("bad cmd structure: %T %v", t, t)
		}
	}
	return c
}

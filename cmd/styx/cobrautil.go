package main

import (
	"log"

	"github.com/spf13/cobra"
	"golang.org/x/exp/slices"
)

type runE = func(*cobra.Command, []string) error

func chainRunE(fs ...runE) runE {
	fs = slices.DeleteFunc(fs, func(e runE) bool { return e == nil })
	if len(fs) == 1 {
		return fs[0]
	}
	return func(c *cobra.Command, args []string) error {
		for _, f := range fs {
			if err := f(c, args); err != nil {
				return err
			}
		}
		return nil
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

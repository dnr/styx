package main

import (
	"context"
	"log"
	"reflect"

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

type ckey struct {
	t reflect.Type
	k any
}

func store[T any](c *cobra.Command, v T) {
	t := reflect.TypeOf((*T)(nil)).Elem()
	c.SetContext(context.WithValue(c.Context(), ckey{t: t}, v))
}

func get[T any](c *cobra.Command) T {
	t := reflect.TypeOf((*T)(nil)).Elem()
	return c.Context().Value(ckey{t: t}).(T)
}

func storeKeyed[T any](c *cobra.Command, v T, key any) {
	t := reflect.TypeOf((*T)(nil)).Elem()
	c.SetContext(context.WithValue(c.Context(), ckey{t: t, k: key}, v))
}

func getKeyed[T any](c *cobra.Command, key any) T {
	t := reflect.TypeOf((*T)(nil)).Elem()
	return c.Context().Value(ckey{t: t, k: key}).(T)
}

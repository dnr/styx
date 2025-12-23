package cobrautil

import (
	"context"
	"log"
	"reflect"
	"slices"

	"github.com/spf13/cobra"
)

type RunE = func(*cobra.Command, []string) error

func ChainRunE(fs ...RunE) RunE {
	fs = slices.DeleteFunc(fs, func(e RunE) bool { return e == nil })
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

func Cmd(c *cobra.Command, stuff ...any) *cobra.Command {
	for _, gthing := range stuff {
		switch thing := gthing.(type) {
		case func(*cobra.Command):
			thing(c)
		case *cobra.Command:
			c.AddCommand(thing)
		case RunE:
			c.RunE = ChainRunE(c.RunE, thing)
		case func(*cobra.Command) RunE:
			c.RunE = ChainRunE(c.RunE, thing(c))
		default:
			log.Panicf("bad Cmd structure: %T %v", thing, thing)
		}
	}
	return c
}

type ckey struct {
	t reflect.Type
	k any
}

func Store[T any](c *cobra.Command, v T) {
	c.SetContext(context.WithValue(c.Context(), ckey{t: reflect.TypeFor[T]()}, v))
}

func Get[T any](c *cobra.Command) T {
	return c.Context().Value(ckey{t: reflect.TypeFor[T]()}).(T)
}

func StoreKeyed[T any](c *cobra.Command, v T, key any) {
	c.SetContext(context.WithValue(c.Context(), ckey{t: reflect.TypeFor[T](), k: key}, v))
}

func GetKeyed[T any](c *cobra.Command, key any) T {
	return c.Context().Value(ckey{t: reflect.TypeFor[T](), k: key}).(T)
}

func Storer[T any](v T) RunE {
	return func(c *cobra.Command, args []string) error {
		Store(c, v)
		return nil
	}
}

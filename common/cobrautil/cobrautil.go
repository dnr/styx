package cobrautil

import (
	"context"
	"log"
	"reflect"
	"slices"

	"github.com/spf13/cobra"
)

// RunE is a Cobra "run" function that returns error.
type RunE = func(c *cobra.Command, args []string) error

// RunEC is like RunE but doesn't take args.
type RunEC = func(c *cobra.Command) error

// ChainRunE returns a RunE that runs its arguments in order and stops on the first error.
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

// ChainRunEC returns a RunEC that runs its arguments in order and stops on the first error.
func ChainRunEC(fs ...RunEC) RunEC {
	fs = slices.DeleteFunc(fs, func(e RunEC) bool { return e == nil })
	if len(fs) == 1 {
		return fs[0]
	}
	return func(c *cobra.Command) error {
		for _, f := range fs {
			if err := f(c); err != nil {
				return err
			}
		}
		return nil
	}
}

// Cmd is a wrapper around cobra.Command to make commands easier to declare in a more
// declarative style. Call it with a base command, followed by things to modify the command.
//
// There's a distinction between "init time" and "run time": things that run before Cmd returns
// are init time, things that run after cobra's Execute are run time. Depending on the types
// provided to Cmd, code will be called at either time:
//
// func(*cobra.Command):
//
//	Generic command filter, called at init time.
//
// *cobra.Command:
//
//	Subcommand: added to base command at init time.
//
// func(*cobra.Command) error:
// func(*cobra.Command, []string) error:
//
//	Action: chained to command's RunE, called at run time. Typically the action will get flag
//	values from c.Context() using Get/GetKeyed.
//
// func(*cobra.Command) func(*cobra.Command) error:
// func(*cobra.Command) func(*cobra.Command, []string) error:
//
//	Filter plus action: call outer function now, chain result to RunE to run at run time.
//	Typically the outer function will add some flags to c.Flags(), and the inner function will
//	store values in c.Context() using Store/StoreKeyed/Storer.
//
// Other function:
//
//	Treated as action: must be a function that accepts 0 or more arguments and returns error.
//	Arguments will be filled in with c, args, c.Context(), and Get(c) as needed using
//	reflection. Think of this like a mini-DI system.
func Cmd(c *cobra.Command, stuff ...any) *cobra.Command {
	for _, gthing := range stuff {
		switch thing := gthing.(type) {
		case func(*cobra.Command):
			thing(c)
		case *cobra.Command:
			// Subcommand: add to parent command
			c.AddCommand(thing)
		case RunE:
			// Action: add to command's RunE
			c.RunE = ChainRunE(c.RunE, thing)
		case RunEC:
			// Action: add to command's RunE
			runE := func(c *cobra.Command, ignored []string) error { return thing(c) }
			c.RunE = ChainRunE(c.RunE, runE)
		case func(*cobra.Command) RunE:
			// Filter plus action: call filter now, add result to RunE
			c.RunE = ChainRunE(c.RunE, thing(c))
		case func(*cobra.Command) RunEC:
			// Filter plus action: call filter now, add result to RunE
			runEC := thing(c)
			runE := func(c *cobra.Command, ignored []string) error { return runEC(c) }
			c.RunE = ChainRunE(c.RunE, runE)
		default:
			// Generic action: try to filter
			v := reflect.ValueOf(thing)
			t := v.Type()
			if t.Kind() != reflect.Func {
				log.Panicf("argument to Cmd %T %v must be function type", thing, thing)
			} else if t.NumOut() != 1 || t.Out(0) != reflect.TypeFor[error]() {
				log.Panicf("generic function %T %v must return error", thing, thing)
			}
			// we don't know what types we'll have available, do the rest at run time
			runE := func(c *cobra.Command, args []string) error {
				ins := make([]reflect.Value, t.NumIn())
				for i := range t.NumIn() {
					switch t.In(i) {
					case reflect.TypeFor[*cobra.Command]():
						ins[i] = reflect.ValueOf(c)
					case reflect.TypeFor[context.Context]():
						ins[i] = reflect.ValueOf(c.Context())
					case reflect.TypeFor[[]string]():
						ins[i] = reflect.ValueOf(args)
					default:
						ins[i] = reflect.ValueOf(c.Context().Value(ckey{t: t.In(i)}))
					}
				}
				return v.Call(ins)[0].Interface().(error)
			}
			c.RunE = ChainRunE(c.RunE, runE)
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
	return func(c *cobra.Command, ignored []string) error {
		Store(c, v)
		return nil
	}
}

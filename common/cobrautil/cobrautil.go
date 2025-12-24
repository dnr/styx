package cobrautil

import (
	"context"
	"fmt"
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
// The general pattern is to define reusable sets of flags or argument handling in filter
// functions and then pass the results through the Command's Context to actions.
//
// There's a distinction between "init time" and "run time": things that run before Cmd returns
// are init time, things that run after cobra's Execute are run time. Note that at init time,
// c.Context() is nil. Depending on the types provided to Cmd, code will be called at either or
// both times:
//
// *cobra.Command:
//
//	Subcommand: added to base command at init time.
//
// func(<anything>) error:
//
//	Action: will be chaned to base command's RunE to run at run time. Must be a function that
//	accepts 0 or more arguments and returns error. Arguments will be filled in with c, args,
//	c.Context(), and Get(c) as needed using reflection. Think of this like a mini-DI system.
//
// func(*cobra.Command):
// func(*cobra.Command) func(<anything>) error:
//
//	Filter [plus action]: call outer function now, chain result to RunE to run at run time.
//	Typically the outer function will add some flags to c.Flags(), and the inner function will
//	store values in c.Context() using Store/StoreKeyed.
func Cmd(c *cobra.Command, stuff ...any) *cobra.Command {
	for _, thing := range stuff {
		if subCmd, ok := thing.(*cobra.Command); ok {
			c.AddCommand(subCmd) // add to parent command
			continue
		}
		v := reflect.ValueOf(thing)
		t := v.Type()
		// Generic action or filter
		if validAction(t) {
			c.RunE = ChainRunE(c.RunE, asAction(v))
		} else if validFilter(t) {
			action := callFilter(v, c)
			c.RunE = ChainRunE(c.RunE, action)
		} else {
			log.Panicf("bad Cmd argument: %T %v", thing, thing)
		}
	}
	return c
}

func validAction(t reflect.Type) bool {
	return t.Kind() == reflect.Func && // must be func
		t.NumOut() == 1 && // must return exactly one value
		// must return error
		t.Out(0) == reflect.TypeFor[error]()
}

func asAction(v reflect.Value) RunE {
	return func(c *cobra.Command, args []string) error {
		t := v.Type()
		ins := make([]reflect.Value, t.NumIn())
		for i := range t.NumIn() {
			tin := t.In(i)
			switch tin {
			case reflect.TypeFor[*cobra.Command]():
				ins[i] = reflect.ValueOf(c)
			case reflect.TypeFor[context.Context]():
				ins[i] = reflect.ValueOf(c.Context())
			case reflect.TypeFor[[]string]():
				ins[i] = reflect.ValueOf(args)
			default:
				in := c.Context().Value(ckey{t: tin})
				if in == nil {
					panic(fmt.Sprintf("couldn't find value for %s in context", tin))
				}
				ins[i] = reflect.ValueOf(in)
			}
		}
		if out := v.Call(ins)[0]; out.IsNil() {
			return nil
		} else {
			return out.Interface().(error)
		}
	}
}

func validFilter(t reflect.Type) bool {
	return t.Kind() == reflect.Func &&
		// filter must accept exactly one arg
		t.NumIn() == 1 &&
		// must accept *cobra.Command
		t.In(0) == reflect.TypeFor[*cobra.Command]() &&
		// don't handle more than one return value (could be added later)
		// disallow 'error' since that would be confused with actions
		(t.NumOut() == 0 || t.Out(0) != reflect.TypeFor[error]())
}

func callFilter(v reflect.Value, c *cobra.Command) RunE {
	out := v.Call([]reflect.Value{reflect.ValueOf(c)})
	if len(out) == 0 {
		return nil // we're done
	}
	o0 := out[0]
	if validAction(o0.Type()) {
		return asAction(o0) // run action at run time
	}
	// store value
	return func(c *cobra.Command, ignored []string) error {
		c.SetContext(context.WithValue(c.Context(), ckey{t: o0.Type()}, o0.Interface()))
		return nil
	}
}

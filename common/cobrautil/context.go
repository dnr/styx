package cobrautil

import (
	"context"
	"reflect"

	"github.com/spf13/cobra"
)

type ckey struct {
	t reflect.Type
	k any
}

// Store stores a value in the Command's context.
func Store[T any](c *cobra.Command, v T) {
	c.SetContext(context.WithValue(c.Context(), ckey{t: reflect.TypeFor[T]()}, v))
}

// Get gets a value from the Command's context.
func Get[T any](c *cobra.Command) T {
	return c.Context().Value(ckey{t: reflect.TypeFor[T]()}).(T)
}

// Store stores a value in the Command's context with an arbitrary key.
func StoreKeyed[T any](c *cobra.Command, v T, key any) {
	c.SetContext(context.WithValue(c.Context(), ckey{t: reflect.TypeFor[T](), k: key}, v))
}

// Get gets a value from the Command's context with an arbitrary key.
func GetKeyed[T any](c *cobra.Command, key any) T {
	return c.Context().Value(ckey{t: reflect.TypeFor[T](), k: key}).(T)
}

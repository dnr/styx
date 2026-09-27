// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package errgroup provides synchronization, error propagation, and Context
// cancelation for groups of goroutines working on subtasks of a common task.
//
// [errgroup.Group] is related to [sync.WaitGroup] but adds handling of tasks
// returning errors.
//
// Modifications for styx:
//   - Embedded Context so can be used as a Context instead of passing both around.
//   - Added Limit() method to get the limit, so that doesn't have to be passed separately either
//     when both are needed.
//   - Added Cancel() to immediately cancel as if a called function returned an error.
//   - Added SetWorkLimit()
//
// Note: use context.Cause(group) to get the pending error without calling Wait().
package errgroup

import (
	"context"
	"fmt"
	"sync"
)

type token struct{}

// A Group is a collection of goroutines working on subtasks that are part of
// the same overall task.
//
// A zero Group is valid, has no limit on the number of active goroutines,
// and does not cancel on error.
type Group struct {
	context.Context

	cancel func(error)

	wg sync.WaitGroup

	sem     chan token
	workSem chan token

	errOnce sync.Once
	err     error
}

func (g *Group) done() {
	if g.workSem != nil {
		<-g.workSem
	}
	if g.sem != nil {
		<-g.sem
	}
	g.wg.Done()
}

// WithContext returns a new Group and an associated Context derived from ctx.
//
// The derived Context is canceled the first time a function passed to Go
// returns a non-nil error or the first time Wait returns, whichever occurs
// first.
func WithContext(ctx context.Context) *Group {
	ctx, cancel := context.WithCancelCause(ctx)
	return &Group{Context: ctx, cancel: cancel}
}

// Wait blocks until all function calls from the Go method have returned, then
// returns the first non-nil error (if any) from them.
func (g *Group) Wait() error {
	g.wg.Wait()
	if g.cancel != nil {
		g.cancel(g.err)
	}
	return g.err
}

// Go calls the given function in a new goroutine.
// It blocks until the new goroutine can be added without the number of
// active goroutines in the group exceeding the configured limit.
//
// The first call to return a non-nil error cancels the group's context, if the
// group was created by calling WithContext. The error will be returned by Wait.
func (g *Group) Go(f func() error) {
	if g.sem != nil {
		g.sem <- token{}
	}

	g._go(f)
}

// TryGo calls the given function in a new goroutine only if the number of
// active goroutines in the group is currently below the configured limit.
//
// The return value reports whether the goroutine was started.
func (g *Group) TryGo(f func() error) bool {
	if g.sem != nil {
		select {
		case g.sem <- token{}:
			// Note: this allows barging iff channels in general allow barging.
		default:
			return false
		}
	}

	g._go(f)
	return true
}

func (g *Group) _go(f func() error) {
	g.wg.Add(1)
	go func() {
		defer g.done()

		if g.workSem != nil {
			g.workSem <- token{}
		}

		if err := f(); err != nil {
			g.Cancel(err)
		}
	}()
}

// SetLimit limits the number of active goroutines in this group to at most n.
// A negative value indicates no limit.
//
// Any subsequent call to the Go method will block until it can add an active
// goroutine without exceeding the configured limit.
//
// The limit must not be modified while any goroutines in the group are active.
func (g *Group) SetLimit(n int) {
	if n < 0 {
		g.sem = nil
		return
	}
	if len(g.sem) != 0 {
		panic(fmt.Errorf("errgroup: modify limit while %v goroutines in the group are still active", len(g.sem)))
	}
	g.sem = make(chan token, n)
}

// SetWorkLimit limits the number of goroutines doing work in this group to at most n.
// Goroutines will be created (Go() will return) without regard to the limit, but blocked on a
// semaphore. A negative value indicates no limit.
func (g *Group) SetWorkLimit(n int) {
	if n < 0 {
		g.workSem = nil
		return
	}
	if len(g.workSem) != 0 {
		panic(fmt.Errorf("errgroup: modify work limit while %v goroutines in the group are still active", len(g.workSem)))
	}
	g.workSem = make(chan token, n)
}

// Limit returns the limit size
func (g *Group) Limit() int {
	return cap(g.sem)
}

// WorkLimit returns the work limit size
func (g *Group) WorkLimit() int {
	return cap(g.workSem)
}

// Cancel cancels the group context with an error, as if a function started by Do returned that error.
func (g *Group) Cancel(err error) {
	g.errOnce.Do(func() {
		g.err = err
		if g.cancel != nil {
			g.cancel(g.err)
		}
	})
}

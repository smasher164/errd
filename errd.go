// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package errd

import (
	"context"
	"errors"
	"fmt"
)

const bufSize = 3

type core struct {
	// Fits into 128 bytes; 2 cache lines on many modern architectures.
	runner   *config
	deferred []deferData
	buf      [bufSize]deferData
	err      *error
	context  context.Context
}

// An E coordinates the error and defer handling.
type E struct{ core }

var errHandlerFirst = errors.New("errd: handler may not be first argument")

// Must causes a call to Run to return on error. An error is detected if err
// is non-nil and if it is still non-nil after passing it to error handling.
func (e *E) Must(err error, h ...Handler) {
	if err != nil {
		processError(e, err, h)
	}
}

// State represents the error state passed to custom error handlers.
type State interface {
	// Context returns the context set by WithContext, or context.TODO
	// otherwise.
	Context() context.Context

	// Panicing reports whether the error resulted from a panic. If true,
	// the panic will be resume after error handling completes. An error handler
	// cannot rewrite an error when panicing.
	Panicing() bool

	// Err reports the first error that passed through an error handler chain.
	// Note that this is always a different error (or nil) than the one passed
	// to an error handler.
	Err() error
}

type state struct{ core }

func (s *state) Context() context.Context {
	if s.context == nil {
		return context.TODO()
	}
	return s.context
}

func (s *state) Panicing() bool { return s.runner.inPanic }

func (s *state) Err() error {
	if s.err == nil {
		return nil
	}
	return *s.err
}

var errOurPanic = errors.New("errd: our panic")

func doRecover(e *E, err *error) {
	switch r := recover(); r {
	case nil:
	case errOurPanic:
		finishDefer(e, err)
		*err = *e.err
	default:
		if !e.runner.inPanic {
			c := *e.runner
			c.inPanic = true
			e.runner = &c
		}
		err2, ok := r.(error)
		if !ok {
			err2 = fmt.Errorf("errd: paniced: %v", r)
		}
		e.err = &err2
		finishDefer(e, err)
		// Check whether there are still defers left to do and then
		// recursively defer.
		panic(r)
	}
}

func doDefers(e *E, barrier int) {
	for len(e.deferred) > barrier {
		i := len(e.deferred) - 1
		d := e.deferred[i]
		e.deferred = e.deferred[:i]
		if d.f == nil {
			continue
		}
		if err := d.f((*state)(e), d.x); err != nil {
			processDeferError(e, err)
		}
	}
}

// finishDefer processes remaining defers after we already have a panic.
// We therefore ignore any panic caught here, knowing that we will panic on an
// older panic after returning.
func finishDefer(e *E, err *error) {
	if len(e.deferred) > 0 {
		defer doRecover(e, err)
		doDefers(e, 0)
	}
}

type errorHandler struct {
	e   *E
	err *error
}

func (h errorHandler) handle(eh Handler) (done bool) {
	newErr := eh.Handle((*state)(h.e), *h.err)
	if newErr == nil {
		return true
	}
	*h.err = newErr
	return false

}

func processErrorSentinel(e *E, err, sentinel error, handlers []Handler) bool {
	eh := errorHandler{e: e, err: &err}
	for _, h := range handlers {
		if eh.handle(h) {
			return false
		}
		if err == sentinel {
			return true
		}
	}
	if len(handlers) == 0 {
		for _, h := range e.runner.defaultHandlers {
			if eh.handle(h) {
				return false
			}
			if err == sentinel {
				return true
			}
		}
	}
	if e.err == nil {
		e.err = &err
	}
	bail(e)
	panic("errd: unreachable")
}

func processDeferError(e *E, err error) {
	eh := errorHandler{e: e, err: &err}
	hadHandler := false
	// Apply handlers added by Defer methods.
	for i := len(e.deferred); i > 0 && e.deferred[i-1].f == nil; i-- {
		hadHandler = true
		// A zero deferred value signals that we have custom defer handler for
		// the subsequent fields.
		if eh.handle(e.deferred[i-1].x.(Handler)) {
			return
		}
	}
	if !hadHandler {
		for _, h := range e.runner.defaultHandlers {
			if eh.handle(h) {
				return
			}
		}
	}
	if e.err == nil {
		e.err = &err
	}
}

func processError(e *E, err error, handlers []Handler) {
	eh := errorHandler{e: e, err: &err}
	for _, h := range handlers {
		if eh.handle(h) {
			return
		}
	}
	if len(handlers) == 0 {
		for _, h := range e.runner.defaultHandlers {
			if eh.handle(h) {
				return
			}
		}
	}
	if e.err == nil {
		e.err = &err
	}
	bail(e)
}

func bail(e *E) {
	// Do defers now and save an extra defer.
	doDefers(e, 0)
	panic(errOurPanic)
}

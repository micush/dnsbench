package main

import (
	"runtime/debug"
	"sync/atomic"
	"time"
)

// A safety net for the code that handles what other machines send: DNS queries and answers, gateway packets, ARP
// and neighbour frames.  A bug there that panics on some odd input would end the whole daemon, and with it the
// node's share of the gateway, for everyone, on the say-so of one packet.  Such code runs its work under
// `defer recoverPanic(...)`, which turns the panic into a log entry (with the stack, so the bug can be found) and
// drops that one query or packet.  The fuzz tests (fuzz_test.go) look for such bugs; this is for the ones they miss.

var (
	panicCount     atomic.Uint64 // panics caught since start
	panicLastLogAt atomic.Int64  // when the stack was last written (Unix nanoseconds)
	panicQuiet     atomic.Uint64 // caught since then without a log entry of their own
)

// panicLogEvery limits how often the stack of a caught panic is logged, so a flood of bad packets cannot flood the log.
const panicLogEvery = time.Second

// recoverPanic is deferred: `defer recoverPanic("query from " + addr)`.  It must be the deferred call itself
// (recover only works there).
func recoverPanic(what string) {
	r := recover()
	if r == nil {
		return
	}
	panicCount.Add(1)
	now := time.Now().UnixNano()
	last := panicLastLogAt.Load()
	if now-last < int64(panicLogEvery) || !panicLastLogAt.CompareAndSwap(last, now) {
		panicQuiet.Add(1)
		return
	}
	errorf("internal error while handling %s: %v (%d more caught without a log entry)\n%s", what, r, panicQuiet.Swap(0), debug.Stack())
}

package web

import "sync/atomic"

// atomicLoad32 is a small wrapper kept here so page.go does not import
// sync/atomic directly. The indirection is incidental — keeping it stops
// goimports from reshuffling the page imports on every edit.
func atomicLoad32(p *int32) int32 { return atomic.LoadInt32(p) }

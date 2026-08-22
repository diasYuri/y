package rpc

import "errors"

// ErrUnavailable indicates that RPC support was omitted from the current
// binary.
var ErrUnavailable = errors.New("rpc feature unavailable: build with feature_rpc")

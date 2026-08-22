package lsp

import "errors"

// ErrUnavailable indicates that LSP support was omitted from the current
// binary.
var ErrUnavailable = errors.New("lsp feature unavailable: build with feature_lsp")

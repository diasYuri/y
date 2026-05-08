//go:build !feature_lsp

package feature

func registerLSPIfCompiled(r *Registry) error { return nil }

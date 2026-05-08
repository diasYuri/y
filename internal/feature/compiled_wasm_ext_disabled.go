//go:build !feature_wasm_ext

package feature

func registerWASMExtensionsIfCompiled(r *Registry) error { return nil }

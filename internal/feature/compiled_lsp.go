//go:build feature_lsp

package feature

func registerLSPIfCompiled(r *Registry) error {
	return r.AddFeature("lsp", "feature_lsp", "Language server integration.")
}

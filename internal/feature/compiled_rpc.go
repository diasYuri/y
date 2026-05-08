//go:build feature_rpc

package feature

func registerRPCIfCompiled(r *Registry) error {
	return r.AddFeature("rpc", "feature_rpc", "RPC/headless mode.")
}

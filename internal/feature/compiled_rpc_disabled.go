//go:build !feature_rpc

package feature

func registerRPCIfCompiled(r *Registry) error { return nil }

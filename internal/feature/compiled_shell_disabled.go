//go:build !feature_shell

package feature

func registerShellIfCompiled(r *Registry) error { return nil }

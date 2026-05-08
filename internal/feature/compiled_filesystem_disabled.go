//go:build !feature_fs

package feature

func registerFilesystemIfCompiled(r *Registry) error { return nil }

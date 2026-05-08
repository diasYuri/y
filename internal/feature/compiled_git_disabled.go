//go:build !feature_git

package feature

func registerGitIfCompiled(r *Registry) error { return nil }

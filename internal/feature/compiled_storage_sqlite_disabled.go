//go:build !feature_storage_sqlite

package feature

func registerStorageSQLiteIfCompiled(r *Registry) error { return nil }

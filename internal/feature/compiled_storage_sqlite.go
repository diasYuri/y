//go:build feature_storage_sqlite

package feature

func registerStorageSQLiteIfCompiled(r *Registry) error {
	return r.AddFeature("storage_sqlite", "feature_storage_sqlite", "SQLite session storage backend.")
}

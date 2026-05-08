//go:build feature_fs

package feature

func registerFilesystemIfCompiled(r *Registry) error {
	if err := r.AddFeature("filesystem", "feature_fs", "Filesystem tools."); err != nil {
		return err
	}
	if err := r.AddTool("read_file", "feature_fs", "Read a file with limits."); err != nil {
		return err
	}
	if err := r.AddTool("write_file", "feature_fs", "Write a file with policy checks."); err != nil {
		return err
	}
	if err := r.AddTool("list_files", "feature_fs", "List files in the workspace."); err != nil {
		return err
	}
	if err := r.AddTool("edit", "feature_fs", "Edit files with audit diffs."); err != nil {
		return err
	}
	if err := r.AddTool("patch", "feature_fs", "Apply unified patches with validation."); err != nil {
		return err
	}
	return r.AddTool("search", "feature_fs", "Search files in the workspace.")
}

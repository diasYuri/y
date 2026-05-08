//go:build feature_git

package feature

func registerGitIfCompiled(r *Registry) error {
	if err := r.AddFeature("git", "feature_git", "Git integration."); err != nil {
		return err
	}
	if err := r.AddTool("git_status", "feature_git", "Read git status."); err != nil {
		return err
	}
	if err := r.AddTool("git_diff", "feature_git", "Read git diff."); err != nil {
		return err
	}
	return r.AddTool("git_commit", "feature_git", "Create a git commit.")
}

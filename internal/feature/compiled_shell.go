//go:build feature_shell

package feature

func registerShellIfCompiled(r *Registry) error {
	if err := r.AddFeature("shell", "feature_shell", "Subprocess execution."); err != nil {
		return err
	}
	return r.AddTool("run_command", "feature_shell", "Run a subprocess with limits.")
}

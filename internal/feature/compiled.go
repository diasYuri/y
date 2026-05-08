package feature

// RegisterCompiledFeatures records every capability present in this binary.
func RegisterCompiledFeatures(r *Registry) error {
	if err := registerCore(r); err != nil {
		return err
	}
	if err := registerAnthropicIfCompiled(r); err != nil {
		return err
	}
	if err := registerFilesystemIfCompiled(r); err != nil {
		return err
	}
	if err := registerGitIfCompiled(r); err != nil {
		return err
	}
	if err := registerGoogleIfCompiled(r); err != nil {
		return err
	}
	if err := registerLocalIfCompiled(r); err != nil {
		return err
	}
	if err := registerLSPIfCompiled(r); err != nil {
		return err
	}
	if err := registerMomIfCompiled(r); err != nil {
		return err
	}
	if err := registerOpenAIIfCompiled(r); err != nil {
		return err
	}
	if err := registerPodsIfCompiled(r); err != nil {
		return err
	}
	if err := registerRPCIfCompiled(r); err != nil {
		return err
	}
	if err := registerShellIfCompiled(r); err != nil {
		return err
	}
	if err := registerStorageSQLiteIfCompiled(r); err != nil {
		return err
	}
	if err := registerTelemetryIfCompiled(r); err != nil {
		return err
	}
	if err := registerWASMExtensionsIfCompiled(r); err != nil {
		return err
	}
	return nil
}

func registerCore(r *Registry) error {
	if err := r.AddCommand("chat", "Start a basic stdin/stdout chat loop."); err != nil {
		return err
	}
	if err := r.AddCommand("config.validate", "Validate declarative configuration."); err != nil {
		return err
	}
	if err := r.AddCommand("features", "List compiled and unavailable capabilities."); err != nil {
		return err
	}
	if err := r.AddCommand("run", "Execute one prompt headlessly and stream text."); err != nil {
		return err
	}
	if err := r.AddCommand("session.list", "List saved sessions for the current workspace."); err != nil {
		return err
	}
	if err := r.AddCommand("session.show", "Print a saved session transcript as JSONL."); err != nil {
		return err
	}
	return r.AddCommand("doctor", "Run basic environment diagnostics.")
}

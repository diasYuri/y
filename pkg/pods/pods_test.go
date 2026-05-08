package pods

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreLoadSave(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(tmp)

	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(cfg.Pods) != 0 {
		t.Fatalf("expected empty pods, got %d", len(cfg.Pods))
	}

	cfg.Pods["test"] = Pod{
		SSH:    "ssh root@1.2.3.4",
		GPUs:   []GPU{{ID: 0, Name: "Tesla V100", Memory: "16 GB"}},
		Models: map[string]Model{},
	}
	cfg.Active = "test"
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg2, err := store.Load()
	if err != nil {
		t.Fatalf("load after save: %v", err)
	}
	if cfg2.Active != "test" {
		t.Fatalf("active mismatch: %q", cfg2.Active)
	}
	if len(cfg2.Pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(cfg2.Pods))
	}
	p := cfg2.Pods["test"]
	if p.SSH != "ssh root@1.2.3.4" {
		t.Fatalf("ssh mismatch: %q", p.SSH)
	}
	if len(p.GPUs) != 1 || p.GPUs[0].Name != "Tesla V100" {
		t.Fatalf("gpu mismatch: %+v", p.GPUs)
	}
}

func TestGetPod(t *testing.T) {
	cfg := Config{
		Pods: map[string]Pod{
			"alpha": {SSH: "ssh alpha"},
			"beta":  {SSH: "ssh beta"},
		},
		Active: "alpha",
	}

	name, pod, err := GetPod(cfg, "")
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if name != "alpha" || pod.SSH != "ssh alpha" {
		t.Fatalf("unexpected active: %s %+v", name, pod)
	}

	name, pod, err = GetPod(cfg, "beta")
	if err != nil {
		t.Fatalf("get beta: %v", err)
	}
	if name != "beta" || pod.SSH != "ssh beta" {
		t.Fatalf("unexpected beta: %s %+v", name, pod)
	}

	_, _, err = GetPod(cfg, "missing")
	if err == nil {
		t.Fatal("expected error for missing pod")
	}

	_, _, err = GetPod(Config{Pods: map[string]Pod{}}, "")
	if err == nil {
		t.Fatal("expected error for no active pod")
	}
}

func TestParseCLIArgs(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		want    CLIArgs
		wantErr bool
	}{
		{
			name: "help",
			argv: []string{"--help"},
			want: CLIArgs{ShowHelp: true},
		},
		{
			name: "version",
			argv: []string{"-v"},
			want: CLIArgs{ShowVersion: true},
		},
		{
			name: "pods list",
			argv: []string{"pods"},
			want: CLIArgs{Command: "pods", Subcommand: "list"},
		},
		{
			name: "pods setup",
			argv: []string{"pods", "setup", "my-pod", "ssh root@1.2.3.4", "--mount", "mount -t nfs /data", "--vllm", "nightly"},
			want: CLIArgs{
				Command:    "pods",
				Subcommand: "setup",
				SetupName:  "my-pod",
				SetupSSH:   "ssh root@1.2.3.4",
				SetupOpts:  SetupOptions{Mount: "mount -t nfs /data", VLLM: "nightly"},
			},
		},
		{
			name: "pods active",
			argv: []string{"pods", "active", "my-pod"},
			want: CLIArgs{Command: "pods", Subcommand: "active", TargetName: "my-pod"},
		},
		{
			name: "pods remove",
			argv: []string{"pods", "remove", "my-pod"},
			want: CLIArgs{Command: "pods", Subcommand: "remove", TargetName: "my-pod"},
		},
		{
			name: "start with options",
			argv: []string{"start", "openai/gpt-oss-20b", "--name", "gpt", "--memory", "50%", "--context", "8k", "--gpus", "2"},
			want: CLIArgs{
				Command:      "start",
				StartModelID: "openai/gpt-oss-20b",
				StartName:    "gpt",
				StartOpts:    StartOptions{Memory: "50%", Context: "8k", GPUs: 2},
			},
		},
		{
			name: "start with vllm args",
			argv: []string{"start", "custom-model", "--name", "m", "--vllm", "--tensor-parallel-size", "4"},
			want: CLIArgs{
				Command:      "start",
				StartModelID: "custom-model",
				StartName:    "m",
				StartOpts:    StartOptions{VLLMArgs: []string{"--tensor-parallel-size", "4"}},
			},
		},
		{
			name: "start missing name",
			argv: []string{"start", "model-id"},
			want: CLIArgs{Command: "start", StartModelID: "model-id"},
		},
		{
			name: "stop all",
			argv: []string{"stop"},
			want: CLIArgs{Command: "stop"},
		},
		{
			name: "stop one",
			argv: []string{"stop", "my-model"},
			want: CLIArgs{Command: "stop", TargetName: "my-model"},
		},
		{
			name: "list models",
			argv: []string{"list"},
			want: CLIArgs{Command: "list", Subcommand: "list-models"},
		},
		{
			name: "logs",
			argv: []string{"logs", "my-model"},
			want: CLIArgs{Command: "logs", TargetName: "my-model"},
		},
		{
			name: "shell",
			argv: []string{"shell", "my-pod"},
			want: CLIArgs{Command: "shell", TargetName: "my-pod"},
		},
		{
			name: "ssh",
			argv: []string{"ssh", "my-pod", "nvidia-smi"},
			want: CLIArgs{Command: "ssh", TargetName: "my-pod", SSHCommand: "nvidia-smi"},
		},
		{
			name: "ssh active",
			argv: []string{"ssh", "nvidia-smi"},
			want: CLIArgs{Command: "ssh", SSHCommand: "nvidia-smi"},
		},
		{
			name: "agent",
			argv: []string{"agent", "my-model", "hello", "world"},
			want: CLIArgs{Command: "agent", TargetName: "my-model", AgentMessages: []string{"hello", "world"}},
		},
		{
			name: "agent continue",
			argv: []string{"agent", "my-model", "--continue", "hello"},
			want: CLIArgs{Command: "agent", TargetName: "my-model", AgentContinue: true, AgentMessages: []string{"hello"}},
		},
		{
			name: "pod override",
			argv: []string{"start", "model", "--name", "n", "--pod", "other"},
			want: CLIArgs{
				Command:      "start",
				StartModelID: "model",
				StartName:    "n",
				PodName:      "other",
			},
		},
		{
			name:    "unknown command",
			argv:    []string{"foo"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCLIArgs(tt.argv)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseCLIArgs(%v) error = %v, wantErr %v", tt.argv, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Command != tt.want.Command {
				t.Errorf("Command = %q, want %q", got.Command, tt.want.Command)
			}
			if got.Subcommand != tt.want.Subcommand {
				t.Errorf("Subcommand = %q, want %q", got.Subcommand, tt.want.Subcommand)
			}
			if got.TargetName != tt.want.TargetName {
				t.Errorf("TargetName = %q, want %q", got.TargetName, tt.want.TargetName)
			}
			if got.StartName != tt.want.StartName {
				t.Errorf("StartName = %q, want %q", got.StartName, tt.want.StartName)
			}
			if got.PodName != tt.want.PodName {
				t.Errorf("PodName = %q, want %q", got.PodName, tt.want.PodName)
			}
		})
	}
}

func TestManagerWithFakeSSH(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(tmp)
	fake := &FakeSSHClient{
		ExecResults: map[string]SSHResult{
			"echo 'SSH OK'": {Stdout: "SSH OK\n", ExitCode: 0},
			"nvidia-smi --query-gpu=index,name,memory.total --format=csv,noheader": {Stdout: "0, NVIDIA H100, 80 GB\n1, NVIDIA H100, 80 GB\n", ExitCode: 0},
		},
		StreamResult: 0,
	}
	mgr := Manager{Store: store, SSH: fake, Getenv: func(k string) string {
		if k == "HF_TOKEN" {
			return "fake-token"
		}
		if k == "PI_API_KEY" {
			return "fake-key"
		}
		return ""
	}}
	ctx := context.Background()

	// Setup a pod.
	err := mgr.SetupPod(ctx, "test-pod", "ssh root@1.2.3.4", SetupOptions{ModelsPath: "/models", VLLM: "release"})
	if err != nil {
		t.Fatalf("setup pod: %v", err)
	}

	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("load after setup: %v", err)
	}
	if cfg.Active != "test-pod" {
		t.Fatalf("expected active test-pod, got %q", cfg.Active)
	}
	if len(cfg.Pods["test-pod"].GPUs) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(cfg.Pods["test-pod"].GPUs))
	}

	// Set active.
	err = mgr.SetActivePod("test-pod")
	if err != nil {
		t.Fatalf("set active: %v", err)
	}

	// Start a known model (use a model that has a 1-GPU config).
	fake.ExecResults["cat > /tmp/model_run_qwen.sh << 'EOF'\n"+strings.Repeat("x", 10)+"\nEOF\nchmod +x /tmp/model_run_qwen.sh"] = SSHResult{ExitCode: 0}
	// Match the start command by prefix since it contains dynamic script content.
	for key := range fake.ExecResults {
		if strings.Contains(key, "model_run_qwen") || strings.Contains(key, "model_wrapper_qwen") {
			delete(fake.ExecResults, key)
		}
	}
	// Register wildcard-ish responses for upload and start commands.
	fake.ExecResults["upload-script"] = SSHResult{ExitCode: 0}
	fake.ExecResults["start-model"] = SSHResult{Stdout: "12345\n", ExitCode: 0}

	// StartModel uses Exec with dynamic commands. Override via ExecFunc.
	fake.ExecResults = map[string]SSHResult{
		"echo 'SSH OK'": {Stdout: "SSH OK\n", ExitCode: 0},
		"nvidia-smi --query-gpu=index,name,memory.total --format=csv,noheader": {Stdout: "0, NVIDIA H100, 80 GB\n", ExitCode: 0},
	}
	fake.ExecFunc = func(ctx context.Context, sshCmd, command string) (SSHResult, error) {
		fake.Calls = append(fake.Calls, FakeSSHCall{Kind: "exec", SSHCmd: sshCmd, Command: command})
		if strings.Contains(command, "model_run_") && strings.Contains(command, "EOF") {
			return SSHResult{ExitCode: 0}, nil
		}
		if strings.Contains(command, "model_wrapper_") {
			return SSHResult{Stdout: "12345\n", ExitCode: 0}, nil
		}
		if r, ok := fake.ExecResults[command]; ok {
			return r, nil
		}
		return SSHResult{ExitCode: 0}, nil
	}

	startOpts := StartOptions{PodName: "test-pod"}
	startOpts.startName = "qwen"
	err = mgr.StartModel(ctx, "Qwen/Qwen2.5-Coder-32B-Instruct", "qwen", startOpts)
	if err != nil {
		t.Fatalf("start model: %v", err)
	}

	cfg, _ = store.Load()
	if len(cfg.Pods["test-pod"].Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(cfg.Pods["test-pod"].Models))
	}
	m := cfg.Pods["test-pod"].Models["qwen"]
	if m.Model != "Qwen/Qwen2.5-Coder-32B-Instruct" {
		t.Fatalf("model ID mismatch: %q", m.Model)
	}
	if m.Port != 8001 {
		t.Fatalf("port mismatch: %d", m.Port)
	}
	if m.PID != 12345 {
		t.Fatalf("PID mismatch: %d", m.PID)
	}

	// List models.
	items, pName, err := mgr.ListModels("test-pod")
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if pName != "test-pod" {
		t.Fatalf("pod name mismatch: %q", pName)
	}
	if len(items) != 1 || items[0].Name != "qwen" {
		t.Fatalf("list items mismatch: %+v", items)
	}

	// Stop model.
	err = mgr.StopModel(ctx, "qwen", "test-pod")
	if err != nil {
		t.Fatalf("stop model: %v", err)
	}
	cfg, _ = store.Load()
	if len(cfg.Pods["test-pod"].Models) != 0 {
		t.Fatalf("expected 0 models after stop, got %d", len(cfg.Pods["test-pod"].Models))
	}

	// Remove pod.
	err = mgr.RemovePod("test-pod")
	if err != nil {
		t.Fatalf("remove pod: %v", err)
	}
	cfg, _ = store.Load()
	if len(cfg.Pods) != 0 {
		t.Fatalf("expected 0 pods after remove, got %d", len(cfg.Pods))
	}
}

func TestManagerStopAllModels(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(tmp)
	fake := &FakeSSHClient{}
	mgr := Manager{Store: store, SSH: fake, Getenv: func(string) string { return "" }}
	ctx := context.Background()

	// Pre-seed config with pod and models.
	cfg := Config{
		Pods: map[string]Pod{
			"pod1": {
				SSH: "ssh root@1.2.3.4",
				Models: map[string]Model{
					"m1": {Model: "model-a", Port: 8001, GPU: []int{0}, PID: 100},
					"m2": {Model: "model-b", Port: 8002, GPU: []int{1}, PID: 200},
				},
			},
		},
		Active: "pod1",
	}
	store.Save(cfg)

	err := mgr.StopAllModels(ctx, "")
	if err != nil {
		t.Fatalf("stop all: %v", err)
	}

	cfg, _ = store.Load()
	if len(cfg.Pods["pod1"].Models) != 0 {
		t.Fatalf("expected 0 models, got %d", len(cfg.Pods["pod1"].Models))
	}
}

func TestSelectGPUs(t *testing.T) {
	pod := Pod{
		GPUs: []GPU{{ID: 0}, {ID: 1}, {ID: 2}, {ID: 3}},
		Models: map[string]Model{
			"m1": {GPU: []int{0}},
			"m2": {GPU: []int{0, 1}},
		},
	}
	got := selectGPUs(pod, 1)
	if len(got) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(got))
	}
	// GPU 2 or 3 should be selected (least used).
	if got[0] != 2 && got[0] != 3 {
		t.Fatalf("expected least-used GPU, got %v", got)
	}

	got = selectGPUs(pod, 4)
	if len(got) != 4 {
		t.Fatalf("expected 4 GPUs, got %d", len(got))
	}
}

func TestNextPort(t *testing.T) {
	pod := Pod{
		Models: map[string]Model{
			"a": {Port: 8001},
			"b": {Port: 8002},
		},
	}
	if nextPort(pod) != 8003 {
		t.Fatalf("expected port 8003, got %d", nextPort(pod))
	}
}

func TestHostFromSSH(t *testing.T) {
	if hostFromSSH("ssh root@1.2.3.4") != "1.2.3.4" {
		t.Fatalf("unexpected host: %q", hostFromSSH("ssh root@1.2.3.4"))
	}
	if hostFromSSH("ssh -p 2222 root@1.2.3.4") != "1.2.3.4" {
		t.Fatalf("unexpected host with port: %q", hostFromSSH("ssh -p 2222 root@1.2.3.4"))
	}
	if hostFromSSH("ssh") != "unknown" {
		t.Fatalf("expected unknown for bare ssh: %q", hostFromSSH("ssh"))
	}
}

func TestFilterOut(t *testing.T) {
	args := []string{"--foo", "bar", "--gpu-memory-utilization", "0.95", "--baz"}
	got := filterOut(args, "gpu-memory-utilization")
	want := []string{"--foo", "bar", "--baz"}
	if len(got) != len(want) {
		t.Fatalf("filterOut: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filterOut[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestKnownModelsCatalog(t *testing.T) {
	if len(KnownModels) == 0 {
		t.Fatal("expected non-empty known models catalog")
	}
	info, ok := KnownModels["Qwen/Qwen2.5-Coder-32B-Instruct"]
	if !ok {
		t.Fatal("expected Qwen model in catalog")
	}
	if info.Name != "Qwen2.5-Coder-32B" {
		t.Fatalf("unexpected name: %q", info.Name)
	}
	if len(info.Configs) == 0 {
		t.Fatal("expected configs")
	}
}

func TestBuildModelRunScript(t *testing.T) {
	script := buildModelRunScript("model-id", "name", 8001, []string{"--arg", "val"})
	if !strings.Contains(script, "model-id") {
		t.Fatal("script missing model ID")
	}
	if !strings.Contains(script, "8001") {
		t.Fatal("script missing port")
	}
	if !strings.Contains(script, "--arg val") {
		t.Fatal("script missing vllm args")
	}
}

func TestBuildEnvExports(t *testing.T) {
	getenv := func(k string) string {
		if k == "HF_TOKEN" {
			return "tok"
		}
		if k == "PI_API_KEY" {
			return "key"
		}
		return ""
	}
	cfg := &ModelConfig{Env: map[string]string{"EXTRA": "1"}}
	out := buildEnvExports(getenv, cfg)
	if !strings.Contains(out, "HF_TOKEN='tok'") {
		t.Fatal("missing HF_TOKEN")
	}
	if !strings.Contains(out, "EXTRA='1'") {
		t.Fatal("missing EXTRA env")
	}
}

func TestStorePath(t *testing.T) {
	store := NewStore("/tmp/test-config")
	if store.Path() != filepath.Join("/tmp/test-config", "pods.json") {
		t.Fatalf("unexpected path: %s", store.Path())
	}
}

func TestFakeSSHClient(t *testing.T) {
	fake := &FakeSSHClient{
		ExecResults: map[string]SSHResult{
			"cmd1": {Stdout: "out1", ExitCode: 0},
		},
		StreamResult: 42,
		SCPErr:       fmt.Errorf("scp error"),
	}
	ctx := context.Background()

	res, err := fake.Exec(ctx, "ssh host", "cmd1")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Stdout != "out1" {
		t.Fatalf("exec stdout: %q", res.Stdout)
	}

	code, err := fake.ExecStream(ctx, "ssh host", "cmd", StreamOpts{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if code != 42 {
		t.Fatalf("stream code: %d", code)
	}

	err = fake.SCP(ctx, "ssh host", "/local", "/remote")
	if err == nil {
		t.Fatal("expected scp error")
	}

	if len(fake.Calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(fake.Calls))
	}
}

func TestManagerLogs(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(tmp)
	fake := &FakeSSHClient{StreamResult: 0}
	mgr := Manager{Store: store, SSH: fake, Getenv: func(string) string { return "" }}
	ctx := context.Background()

	cfg := Config{
		Pods: map[string]Pod{
			"pod1": {
				SSH:    "ssh root@1.2.3.4",
				Models: map[string]Model{"m1": {Model: "model-a", Port: 8001, GPU: []int{0}, PID: 100}},
			},
		},
		Active: "pod1",
	}
	store.Save(cfg)

	err := mgr.Logs(ctx, "", "m1", LogsOptions{Follow: false, Lines: 50})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if !strings.Contains(fake.Calls[0].Command, "tail -n 50") {
		t.Fatalf("expected tail -n 50, got %q", fake.Calls[0].Command)
	}
	if !strings.Contains(fake.Calls[0].Command, "~/.vllm_logs/m1.log") {
		t.Fatalf("expected log path, got %q", fake.Calls[0].Command)
	}
}

func TestManagerLogsFollow(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(tmp)
	fake := &FakeSSHClient{StreamResult: 0}
	mgr := Manager{Store: store, SSH: fake, Getenv: func(string) string { return "" }}
	ctx := context.Background()

	cfg := Config{
		Pods: map[string]Pod{
			"pod1": {
				SSH:    "ssh root@1.2.3.4",
				Models: map[string]Model{"m1": {Model: "model-a", Port: 8001}},
			},
		},
		Active: "pod1",
	}
	store.Save(cfg)

	err := mgr.Logs(ctx, "", "m1", LogsOptions{Follow: true})
	if err != nil {
		t.Fatalf("logs follow: %v", err)
	}
	if !strings.Contains(fake.Calls[0].Command, "tail -F") {
		t.Fatalf("expected tail -F, got %q", fake.Calls[0].Command)
	}
}

func TestManagerLogsModelNotFound(t *testing.T) {
	tmp := t.TempDir()
	store := NewStore(tmp)
	fake := &FakeSSHClient{}
	mgr := Manager{Store: store, SSH: fake, Getenv: func(string) string { return "" }}
	ctx := context.Background()

	cfg := Config{
		Pods:   map[string]Pod{"pod1": {SSH: "ssh root@1.2.3.4", Models: map[string]Model{}}},
		Active: "pod1",
	}
	store.Save(cfg)

	err := mgr.Logs(ctx, "", "missing", LogsOptions{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected model not found, got %v", err)
	}
}

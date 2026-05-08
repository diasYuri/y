package pods

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Manager holds pod management operations.
type Manager struct {
	Store  *Store
	SSH    SSHClient
	Getenv func(string) string
}

// ListPods returns all configured pods.
func (m *Manager) ListPods() (Config, error) {
	return m.Store.Load()
}

// SetupOptions configures pod setup.
type SetupOptions struct {
	Mount      string
	ModelsPath string
	VLLM       string
}

// SetupPod adds a new pod after probing GPUs.
func (m *Manager) SetupPod(ctx context.Context, name, sshCmd string, opts SetupOptions) error {
	if name == "" || sshCmd == "" {
		return fmt.Errorf("name and ssh command are required")
	}
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}

	modelsPath := opts.ModelsPath
	if modelsPath == "" && opts.Mount != "" {
		parts := strings.Fields(opts.Mount)
		last := parts[len(parts)-1]
		if strings.HasPrefix(last, "/") {
			modelsPath = last
		}
	}
	if modelsPath == "" {
		return fmt.Errorf("--models-path is required (or must be extractable from --mount)")
	}

	// Test SSH connection.
	res, err := m.SSH.Exec(ctx, sshCmd, "echo 'SSH OK'")
	if err != nil {
		return fmt.Errorf("ssh test failed: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("ssh test failed: %s", res.Stderr)
	}

	// Detect GPUs.
	gpuRes, err := m.SSH.Exec(ctx, sshCmd, "nvidia-smi --query-gpu=index,name,memory.total --format=csv,noheader")
	var gpus []GPU
	if err == nil && gpuRes.ExitCode == 0 {
		for _, line := range strings.Split(strings.TrimSpace(gpuRes.Stdout), "\n") {
			parts := strings.Split(line, ",")
			if len(parts) >= 3 {
				id := 0
				fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &id)
				gpus = append(gpus, GPU{
					ID:     id,
					Name:   strings.TrimSpace(parts[1]),
					Memory: strings.TrimSpace(parts[2]),
				})
			}
		}
	}

	vllm := opts.VLLM
	if vllm == "" {
		vllm = "release"
	}

	cfg.Pods[name] = Pod{
		SSH:         sshCmd,
		GPUs:        gpus,
		Models:      map[string]Model{},
		ModelsPath:  modelsPath,
		VLLMVersion: vllm,
	}
	if cfg.Active == "" {
		cfg.Active = name
	}
	return m.Store.Save(cfg)
}

// SetActivePod sets the active pod.
func (m *Manager) SetActivePod(name string) error {
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Pods[name]; !ok {
		return fmt.Errorf("pod %q not found", name)
	}
	cfg.Active = name
	return m.Store.Save(cfg)
}

// RemovePod removes a pod from config.
func (m *Manager) RemovePod(name string) error {
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Pods[name]; !ok {
		return fmt.Errorf("pod %q not found", name)
	}
	delete(cfg.Pods, name)
	if cfg.Active == name {
		cfg.Active = ""
	}
	return m.Store.Save(cfg)
}

// StartOptions configures model start.
type StartOptions struct {
	PodName   string
	Memory    string
	Context   string
	GPUs      int
	VLLMArgs  []string
	startName string // parsed by CLI, exposed via CLIArgs.StartName
}

// StartModel starts a vLLM model on a pod.
func (m *Manager) StartModel(ctx context.Context, modelID, name string, opts StartOptions) error {
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}
	podName, pod, err := GetPod(cfg, opts.PodName)
	if err != nil {
		return err
	}
	if pod.ModelsPath == "" {
		return fmt.Errorf("pod %q has no models path configured", podName)
	}
	if _, exists := pod.Models[name]; exists {
		return fmt.Errorf("model %q already exists on pod %q", name, podName)
	}

	port := nextPort(pod)
	var gpus []int
	var vllmArgs []string
	var modelCfg *ModelConfig

	if len(opts.VLLMArgs) > 0 {
		vllmArgs = opts.VLLMArgs
	} else if info, ok := KnownModels[modelID]; ok {
		if opts.GPUs > 0 {
			if opts.GPUs > len(pod.GPUs) {
				return fmt.Errorf("requested %d GPUs but pod only has %d", opts.GPUs, len(pod.GPUs))
			}
			for _, c := range info.Configs {
				if c.GPUCount == opts.GPUs {
					modelCfg = &c
					break
				}
			}
			if modelCfg == nil {
				return fmt.Errorf("model %q has no config for %d GPU(s)", info.Name, opts.GPUs)
			}
			gpus = selectGPUs(pod, opts.GPUs)
			vllmArgs = append([]string(nil), modelCfg.Args...)
		} else {
			for count := len(pod.GPUs); count >= 1; count-- {
				for _, c := range info.Configs {
					if c.GPUCount == count {
						modelCfg = &c
						break
					}
				}
				if modelCfg != nil {
					gpus = selectGPUs(pod, count)
					vllmArgs = append([]string(nil), modelCfg.Args...)
					break
				}
			}
			if modelCfg == nil {
				return fmt.Errorf("model %q is not compatible with this pod's GPUs", info.Name)
			}
		}
	} else {
		if opts.GPUs > 0 {
			return fmt.Errorf("--gpus can only be used with predefined models")
		}
		gpus = selectGPUs(pod, 1)
	}

	// Apply memory/context overrides.
	if len(opts.VLLMArgs) == 0 {
		if opts.Memory != "" {
			var fraction float64
			fmt.Sscanf(opts.Memory, "%f%%", &fraction)
			vllmArgs = filterOut(vllmArgs, "gpu-memory-utilization")
			vllmArgs = append(vllmArgs, "--gpu-memory-utilization", fmt.Sprintf("%.2f", fraction/100))
		}
		if opts.Context != "" {
			sizes := map[string]int{"4k": 4096, "8k": 8192, "16k": 16384, "32k": 32768, "64k": 65536, "128k": 131072}
			maxTokens := sizes[strings.ToLower(opts.Context)]
			if maxTokens == 0 {
				fmt.Sscanf(opts.Context, "%d", &maxTokens)
			}
			vllmArgs = filterOut(vllmArgs, "max-model-len")
			vllmArgs = append(vllmArgs, "--max-model-len", fmt.Sprintf("%d", maxTokens))
		}
	}

	// Generate and upload run script.
	script := buildModelRunScript(modelID, name, port, vllmArgs)
	uploadCmd := fmt.Sprintf("cat > /tmp/model_run_%s.sh << 'EOF'\n%s\nEOF\nchmod +x /tmp/model_run_%s.sh", name, script, name)
	if _, err := m.SSH.Exec(ctx, pod.SSH, uploadCmd); err != nil {
		return fmt.Errorf("failed to upload run script: %w", err)
	}

	// Build environment and start.
	envExports := buildEnvExports(m.Getenv, modelCfg)
	startCmd := fmt.Sprintf(`%s
mkdir -p ~/.vllm_logs
cat > /tmp/model_wrapper_%s.sh << 'WRAPPER'
#!/bin/bash
script -q -f -c "/tmp/model_run_%s.sh" ~/.vllm_logs/%s.log
exit_code=$?
echo "Script exited with code $exit_code" >> ~/.vllm_logs/%s.log
exit $exit_code
WRAPPER
chmod +x /tmp/model_wrapper_%s.sh
setsid /tmp/model_wrapper_%s.sh </dev/null >/dev/null 2>&1 &
echo $!
exit 0`, envExports, name, name, name, name, name, name)

	pidRes, err := m.SSH.Exec(ctx, pod.SSH, startCmd)
	if err != nil {
		return fmt.Errorf("failed to start model: %w", err)
	}
	var pid int
	fmt.Sscanf(strings.TrimSpace(pidRes.Stdout), "%d", &pid)
	if pid == 0 {
		return fmt.Errorf("failed to start model: no PID returned")
	}

	// Save state.
	pod.Models[name] = Model{
		Model: modelID,
		Port:  port,
		GPU:   gpus,
		PID:   pid,
	}
	cfg.Pods[podName] = pod
	return m.Store.Save(cfg)
}

// StopModel stops a running model.
func (m *Manager) StopModel(ctx context.Context, name, podName string) error {
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}
	pName, pod, err := GetPod(cfg, podName)
	if err != nil {
		return err
	}
	model, ok := pod.Models[name]
	if !ok {
		return fmt.Errorf("model %q not found on pod %q", name, pName)
	}
	killCmd := fmt.Sprintf("pkill -TERM -P %d 2>/dev/null || true; kill %d 2>/dev/null || true", model.PID, model.PID)
	m.SSH.Exec(ctx, pod.SSH, killCmd)
	delete(cfg.Pods[pName].Models, name)
	return m.Store.Save(cfg)
}

// StopAllModels stops all models on a pod.
func (m *Manager) StopAllModels(ctx context.Context, podName string) error {
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}
	pName, pod, err := GetPod(cfg, podName)
	if err != nil {
		return err
	}
	var pids []string
	for _, model := range pod.Models {
		pids = append(pids, fmt.Sprintf("%d", model.PID))
	}
	if len(pids) > 0 {
		killCmd := fmt.Sprintf("for PID in %s; do pkill -TERM -P $PID 2>/dev/null || true; kill $PID 2>/dev/null || true; done", strings.Join(pids, " "))
		m.SSH.Exec(ctx, pod.SSH, killCmd)
	}
	pod.Models = map[string]Model{}
	cfg.Pods[pName] = pod
	return m.Store.Save(cfg)
}

// ModelListItem describes a model for listing.
type ModelListItem struct {
	Name  string
	Model string
	Port  int
	GPU   []int
	PID   int
	Host  string
}

// ListModels returns all models on a pod.
func (m *Manager) ListModels(podName string) ([]ModelListItem, string, error) {
	cfg, err := m.Store.Load()
	if err != nil {
		return nil, "", err
	}
	pName, pod, err := GetPod(cfg, podName)
	if err != nil {
		return nil, "", err
	}
	var items []ModelListItem
	for name, model := range pod.Models {
		items = append(items, ModelListItem{
			Name:  name,
			Model: model.Model,
			Port:  model.Port,
			GPU:   model.GPU,
			PID:   model.PID,
			Host:  hostFromSSH(pod.SSH),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, pName, nil
}

// Shell opens an interactive SSH shell.
func (m *Manager) Shell(ctx context.Context, podName string) error {
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}
	_, pod, err := GetPod(cfg, podName)
	if err != nil {
		return err
	}
	_, err = m.SSH.ExecStream(ctx, pod.SSH, "", StreamOpts{ForceTTY: true})
	return err
}

// LogsOptions configures log streaming.
type LogsOptions struct {
	Follow bool
	Lines  int
}

// Logs streams model logs from a pod.
func (m *Manager) Logs(ctx context.Context, podName, modelName string, opts LogsOptions) error {
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}
	_, pod, err := GetPod(cfg, podName)
	if err != nil {
		return err
	}
	_, ok := pod.Models[modelName]
	if !ok {
		return fmt.Errorf("model %q not found on pod %q", modelName, podName)
	}
	lines := opts.Lines
	if lines <= 0 {
		lines = 100
	}
	flag := "-n"
	if opts.Follow {
		flag = "-F"
	}
	cmd := fmt.Sprintf("tail %s %d ~/.vllm_logs/%s.log 2>/dev/null || echo 'No logs found for model %s'", flag, lines, modelName, modelName)
	_, err = m.SSH.ExecStream(ctx, pod.SSH, cmd, StreamOpts{Silent: false})
	return err
}

// AgentOptions configures agent chat.
type AgentOptions struct {
	Messages []string
	Continue bool
}

// Agent sends chat messages to a running vLLM model.
func (m *Manager) Agent(ctx context.Context, podName, modelName string, opts AgentOptions) error {
	cfg, err := m.Store.Load()
	if err != nil {
		return err
	}
	_, pod, err := GetPod(cfg, podName)
	if err != nil {
		return err
	}
	model, ok := pod.Models[modelName]
	if !ok {
		return fmt.Errorf("model %q not found on pod %q", modelName, podName)
	}

	host := hostFromSSH(pod.SSH)
	url := fmt.Sprintf("http://%s:%d/v1/chat/completions", host, model.Port)

	var messages []map[string]string
	for _, msg := range opts.Messages {
		messages = append(messages, map[string]string{"role": "user", "content": msg})
	}

	body := map[string]interface{}{
		"model":    model.Model,
		"messages": messages,
		"stream":   false,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key := m.Getenv("PI_API_KEY"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("vLLM returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return fmt.Errorf("no response from model")
	}
	fmt.Println(result.Choices[0].Message.Content)
	return nil
}

// SSHExec runs a command on a pod.
func (m *Manager) SSHExec(ctx context.Context, podName, command string) (SSHResult, error) {
	cfg, err := m.Store.Load()
	if err != nil {
		return SSHResult{}, err
	}
	_, pod, err := GetPod(cfg, podName)
	if err != nil {
		return SSHResult{}, err
	}
	return m.SSH.Exec(ctx, pod.SSH, command)
}

// --- helpers ---

func nextPort(pod Pod) int {
	used := map[int]bool{}
	for _, m := range pod.Models {
		used[m.Port] = true
	}
	port := 8001
	for used[port] {
		port++
	}
	return port
}

func selectGPUs(pod Pod, count int) []int {
	if count == len(pod.GPUs) {
		out := make([]int, len(pod.GPUs))
		for i, g := range pod.GPUs {
			out[i] = g.ID
		}
		return out
	}
	usage := map[int]int{}
	for _, g := range pod.GPUs {
		usage[g.ID] = 0
	}
	for _, m := range pod.Models {
		for _, gid := range m.GPU {
			usage[gid]++
		}
	}
	type pair struct{ id, count int }
	var pairs []pair
	for id, c := range usage {
		pairs = append(pairs, pair{id, c})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].count < pairs[j].count })
	out := make([]int, 0, count)
	for i := 0; i < count && i < len(pairs); i++ {
		out = append(out, pairs[i].id)
	}
	return out
}

func filterOut(args []string, needle string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if strings.Contains(args[i], needle) {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func hostFromSSH(sshCmd string) string {
	for _, p := range strings.Fields(sshCmd) {
		if strings.Contains(p, "@") {
			parts := strings.SplitN(p, "@", 2)
			if len(parts) == 2 {
				return parts[1]
			}
		}
	}
	return "unknown"
}

func buildEnvExports(getenv func(string) string, cfg *ModelConfig) string {
	var lines []string
	add := func(k, v string) {
		lines = append(lines, fmt.Sprintf("export %s='%s'", k, v))
	}
	add("HF_TOKEN", getenv("HF_TOKEN"))
	add("PI_API_KEY", getenv("PI_API_KEY"))
	add("HF_HUB_ENABLE_HF_TRANSFER", "1")
	add("VLLM_NO_USAGE_STATS", "1")
	add("PYTORCH_CUDA_ALLOC_CONF", "expandable_segments:True")
	add("FORCE_COLOR", "1")
	add("TERM", "xterm-256color")
	if cfg != nil {
		for k, v := range cfg.Env {
			add(k, v)
		}
	}
	return strings.Join(lines, "\n")
}

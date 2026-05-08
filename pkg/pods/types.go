package pods

// GPU describes a single GPU on a remote pod.
type GPU struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Memory string `json:"memory"`
}

// Model describes a running vLLM model instance on a pod.
type Model struct {
	Model string `json:"model"`
	Port  int    `json:"port"`
	GPU   []int  `json:"gpu"`
	PID   int    `json:"pid"`
}

// Pod describes a remote GPU pod reachable via SSH.
type Pod struct {
	SSH         string           `json:"ssh"`
	GPUs        []GPU            `json:"gpus"`
	Models      map[string]Model `json:"models"`
	ModelsPath  string           `json:"modelsPath,omitempty"`
	VLLMVersion string           `json:"vllmVersion,omitempty"`
}

// Config is the persisted pods configuration.
type Config struct {
	Pods   map[string]Pod `json:"pods"`
	Active string         `json:"active,omitempty"`
}

// SSHResult is the result of an SSH command execution.
type SSHResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ModelConfig describes hardware requirements for a known model.
type ModelConfig struct {
	GPUCount int               `json:"gpuCount"`
	GPUTypes []string          `json:"gpuTypes"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env,omitempty"`
	Notes    string            `json:"notes,omitempty"`
}

// KnownModelInfo describes a model in the built-in catalog.
type KnownModelInfo struct {
	Name    string        `json:"name"`
	Configs []ModelConfig `json:"configs"`
	Notes   string        `json:"notes,omitempty"`
}

// KnownModels is the built-in model catalog.
var KnownModels = map[string]KnownModelInfo{
	"Qwen/Qwen2.5-Coder-32B-Instruct": {
		Name: "Qwen2.5-Coder-32B",
		Configs: []ModelConfig{
			{GPUCount: 1, GPUTypes: []string{"H100", "H200"}, Args: []string{"--tool-call-parser", "hermes", "--enable-auto-tool-choice"}},
			{GPUCount: 2, GPUTypes: []string{"H100", "H200"}, Args: []string{"--tensor-parallel-size", "2", "--tool-call-parser", "hermes", "--enable-auto-tool-choice"}},
		},
	},
	"Qwen/Qwen3-Coder-30B-A3B-Instruct": {
		Name: "Qwen3-Coder-30B",
		Configs: []ModelConfig{
			{GPUCount: 1, GPUTypes: []string{"H100", "H200"}, Args: []string{"--enable-auto-tool-choice", "--tool-call-parser", "qwen3_coder"}, Notes: "Fits comfortably on single GPU. ~60GB model weight."},
			{GPUCount: 2, GPUTypes: []string{"H100", "H200"}, Args: []string{"--tensor-parallel-size", "2", "--enable-auto-tool-choice", "--tool-call-parser", "qwen3_coder"}, Notes: "For higher throughput/longer context."},
		},
	},
	"Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8": {
		Name: "Qwen3-Coder-30B-FP8",
		Configs: []ModelConfig{
			{GPUCount: 1, GPUTypes: []string{"H100", "H200"}, Args: []string{"--enable-auto-tool-choice", "--tool-call-parser", "qwen3_coder"}, Env: map[string]string{"VLLM_USE_DEEP_GEMM": "1"}, Notes: "FP8 quantized, ~30GB model weight. Excellent for single GPU deployment."},
		},
	},
	"Qwen/Qwen3-Coder-480B-A35B-Instruct": {
		Name: "Qwen3-Coder-480B",
		Configs: []ModelConfig{
			{GPUCount: 8, GPUTypes: []string{"H200", "H20"}, Args: []string{"--tensor-parallel-size", "8", "--max-model-len", "32000", "--enable-auto-tool-choice", "--tool-call-parser", "qwen3_coder"}, Notes: "Cannot serve full 262K context on single node. Reduce max-model-len or increase gpu-memory-utilization."},
		},
	},
	"Qwen/Qwen3-Coder-480B-A35B-Instruct-FP8": {
		Name: "Qwen3-Coder-480B-FP8",
		Configs: []ModelConfig{
			{GPUCount: 8, GPUTypes: []string{"H200", "H20"}, Args: []string{"--max-model-len", "131072", "--enable-expert-parallel", "--data-parallel-size", "8", "--enable-auto-tool-choice", "--tool-call-parser", "qwen3_coder"}, Env: map[string]string{"VLLM_USE_DEEP_GEMM": "1"}, Notes: "Use data-parallel mode (not tensor-parallel) to avoid weight quantization errors."},
		},
	},
	"openai/gpt-oss-20b": {
		Name: "GPT-OSS-20B",
		Configs: []ModelConfig{
			{GPUCount: 1, GPUTypes: []string{"H100", "H200"}, Args: []string{"--async-scheduling"}},
			{GPUCount: 1, GPUTypes: []string{"B200"}, Args: []string{"--async-scheduling"}, Env: map[string]string{"VLLM_USE_TRTLLM_ATTENTION": "1", "VLLM_USE_TRTLLM_DECODE_ATTENTION": "1", "VLLM_USE_TRTLLM_CONTEXT_ATTENTION": "1", "VLLM_USE_FLASHINFER_MXFP4_MOE": "1"}},
		},
		Notes: "Tools/function calls only via /v1/responses endpoint.",
	},
	"openai/gpt-oss-120b": {
		Name: "GPT-OSS-120B",
		Configs: []ModelConfig{
			{GPUCount: 1, GPUTypes: []string{"H100", "H200"}, Args: []string{"--async-scheduling", "--gpu-memory-utilization", "0.95", "--max-num-batched-tokens", "1024"}, Notes: "Single GPU deployment. Tools/function calls only via /v1/responses endpoint."},
			{GPUCount: 2, GPUTypes: []string{"H100", "H200"}, Args: []string{"--tensor-parallel-size", "2", "--async-scheduling", "--gpu-memory-utilization", "0.94"}, Notes: "Recommended for H100/H200. Tools/function calls only via /v1/responses endpoint."},
			{GPUCount: 4, GPUTypes: []string{"H100", "H200"}, Args: []string{"--tensor-parallel-size", "4", "--async-scheduling"}, Notes: "Higher throughput. Tools/function calls only via /v1/responses endpoint."},
			{GPUCount: 8, GPUTypes: []string{"H100", "H200"}, Args: []string{"--tensor-parallel-size", "8", "--async-scheduling"}, Notes: "Maximum throughput for evaluation workloads. Tools/function calls only via /v1/responses endpoint."},
		},
	},
	"zai-org/GLM-4.5": {
		Name: "GLM-4.5",
		Configs: []ModelConfig{
			{GPUCount: 16, GPUTypes: []string{"H100"}, Args: []string{"--tensor-parallel-size", "16", "--tool-call-parser", "glm45", "--reasoning-parser", "glm45", "--enable-auto-tool-choice"}},
			{GPUCount: 8, GPUTypes: []string{"H200"}, Args: []string{"--tensor-parallel-size", "8", "--tool-call-parser", "glm45", "--reasoning-parser", "glm45", "--enable-auto-tool-choice"}},
		},
		Notes: "Models default to thinking mode. For full 128K context, double the GPU count.",
	},
	"zai-org/GLM-4.5-FP8": {
		Name: "GLM-4.5-FP8",
		Configs: []ModelConfig{
			{GPUCount: 8, GPUTypes: []string{"H100"}, Args: []string{"--tensor-parallel-size", "8", "--tool-call-parser", "glm45", "--reasoning-parser", "glm45", "--enable-auto-tool-choice"}},
			{GPUCount: 4, GPUTypes: []string{"H200"}, Args: []string{"--tensor-parallel-size", "4", "--tool-call-parser", "glm45", "--reasoning-parser", "glm45", "--enable-auto-tool-choice"}},
		},
	},
	"zai-org/GLM-4.5-Air-FP8": {
		Name: "GLM-4.5-Air-FP8",
		Configs: []ModelConfig{
			{GPUCount: 2, GPUTypes: []string{"H100"}, Args: []string{"--tensor-parallel-size", "2", "--tool-call-parser", "glm45", "--reasoning-parser", "glm45", "--enable-auto-tool-choice"}, Env: map[string]string{"VLLM_ATTENTION_BACKEND": "XFORMERS"}, Notes: "FP8 model requires vLLM with proper FP8 support or MTP module"},
			{GPUCount: 1, GPUTypes: []string{"H200"}, Args: []string{"--tool-call-parser", "glm45", "--reasoning-parser", "glm45", "--enable-auto-tool-choice"}, Env: map[string]string{"VLLM_ATTENTION_BACKEND": "XFORMERS"}, Notes: "FP8 model requires vLLM with proper FP8 support or MTP module"},
		},
	},
	"zai-org/GLM-4.5-Air": {
		Name: "GLM-4.5-Air",
		Configs: []ModelConfig{
			{GPUCount: 2, GPUTypes: []string{"H100", "H200"}, Args: []string{"--tensor-parallel-size", "2", "--tool-call-parser", "glm45", "--reasoning-parser", "glm45", "--enable-auto-tool-choice"}, Notes: "Non-quantized BF16 version, more compatible"},
			{GPUCount: 1, GPUTypes: []string{"H200"}, Args: []string{"--tool-call-parser", "glm45", "--reasoning-parser", "glm45", "--enable-auto-tool-choice", "--gpu-memory-utilization", "0.95"}, Notes: "Single H200 can fit the BF16 model with high memory utilization"},
		},
	},
	"moonshotai/Kimi-K2-Instruct": {
		Name: "Kimi-K2",
		Configs: []ModelConfig{
			{GPUCount: 16, GPUTypes: []string{"H200", "H20"}, Args: []string{"--tensor-parallel-size", "16", "--trust-remote-code", "--enable-auto-tool-choice", "--tool-call-parser", "kimi_k2"}, Notes: "Pure TP mode. For >16 GPUs, combine with pipeline-parallelism."},
		},
		Notes: "Requires vLLM v0.10.0rc1+. Minimum 16 GPUs for FP8 with 128k context.",
	},
}

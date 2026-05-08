# Provider behavior matrix

Activity: `phase-0-baseline-behavior`

Status values:

- `preserve`: keep equivalent provider behavior in Go.
- `planned-change`: intentionally alter wiring to match build tags, config validation or command naming.
- `gap`: requires deeper provider-specific verification before implementation parity.

## Provider architecture

| Behavior | Current behavior in `pi-mono` | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|
| Provider registry | API adapters are registered globally at module load; provider modules are lazy-imported and forward events into `AssistantMessageEventStream`. | planned-change | `packages/ai/src/api-registry.ts`, `packages/ai/src/providers/register-builtins.ts`, `packages/ai/src/stream.ts` | `pkg/providers` explicit registry from bootstrap, split by build tags |
| Stream contract | `stream` and `streamSimple` return `AssistantMessageEventStream`; failures should become stream `error` events with assistant messages instead of thrown request failures. | preserve | `packages/ai/src/types.ts`, `packages/ai/src/utils/event-stream.ts`, `packages/agent/src/types.ts` | `pkg/ai` `EventStream.Next(ctx)`, normalized provider events |
| Message model | Shared content supports text, thinking, image, tool call, tool result, usage, costs, response IDs and provider/model metadata. | preserve | `packages/ai/src/types.ts` | `pkg/ai` typed structs; provider-specific fields in typed optional fields or `json.RawMessage` outside hot paths |
| Reasoning/thinking | Reasoning levels are `minimal`, `low`, `medium`, `high`, `xhigh`; coding-agent also exposes `off` and downgrades unsupported `xhigh`. | preserve | `packages/ai/src/types.ts`, `packages/coding-agent/src/cli/args.ts`, `packages/coding-agent/src/main.ts` | `pkg/ai`, `pkg/coding/models` |
| Tool-call compatibility | Providers normalize tool calls/results, partial JSON and provider-specific tool call ID requirements. | preserve | `packages/ai/src/providers/*`, `packages/ai/src/providers/google-shared.ts`, `packages/ai/src/providers/openai-responses-shared.ts`, `packages/ai/src/providers/transform-messages.ts` | `pkg/providers/transform`, provider-specific stream parsers |
| Auth resolution | Auth priority includes stored auth, runtime override, env vars, `models.json` direct/command values, provider OAuth, Google ADC and AWS ambient credentials. | preserve | `packages/coding-agent/src/core/auth-storage.ts`, `packages/coding-agent/src/core/model-registry.ts`, `packages/ai/src/env-api-keys.ts` | `internal/auth`, `pkg/providers/auth` |
| Custom models/providers | `models.json` can add models, override built-ins, set base URLs/headers/compat, register dynamic extension providers and OAuth providers. | gap | `packages/coding-agent/src/core/model-registry.ts`, `packages/coding-agent/src/core/extensions/types.ts` | Static Go config plus optional WASM provider support deferred; V1 should cover OpenAI-compatible custom endpoints |
| Provider tests | Legacy tests cover aborts, SSE parsing, OAuth, prompt cache, tool IDs, images, unicode, overflow and provider-specific compatibility. | preserve | `packages/ai/test/*` | Go fake HTTP/SSE tests per provider package |

## Built-in API adapters

| API adapter | Provider families covered today | Current behavior and auth | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|---|
| `openai-responses` | `openai` and compatible Responses users | Streams OpenAI Responses events, usage, prompt cache/session affinity, tool results including images. Auth via `OPENAI_API_KEY`, stored key or runtime override. | preserve | `packages/ai/src/providers/openai-responses.ts`, `packages/ai/src/providers/openai-responses-shared.ts`, `packages/ai/test/openai-responses-*.test.ts` | `pkg/providers/openai` behind `feature_openai` |
| `openai-codex-responses` | `openai-codex` | Responses variant with ChatGPT/Codex OAuth support and cache/reasoning behavior. | preserve | `packages/ai/src/providers/openai-codex-responses.ts`, `packages/ai/src/utils/oauth/openai-codex.ts`, `packages/ai/test/openai-codex-*.test.ts` | `pkg/providers/openai` or `pkg/providers/codex`; OAuth optional |
| `openai-completions` | OpenAI-compatible chat completions: OpenRouter, DeepSeek, Groq, xAI, Cerebras, Vercel AI Gateway, z.ai, Cloudflare, local compatible endpoints and custom models | Handles compatibility flags for developer role, reasoning formats, cache control, usage in streaming, max token field, tool result name, assistant-after-tool-result and strict mode. | preserve | `packages/ai/src/providers/openai-completions.ts`, `packages/ai/src/types.ts`, `packages/coding-agent/src/core/model-registry.ts` | `pkg/providers/compatible`, optionally reused by `pkg/providers/openai` |
| `anthropic-messages` | `anthropic` and Anthropic-compatible routes | Streams Anthropic Messages, thinking, eager tool input compatibility, cache retention and OAuth/API key auth. | preserve | `packages/ai/src/providers/anthropic.ts`, `packages/ai/src/utils/oauth/anthropic.ts`, `packages/ai/test/anthropic-*.test.ts` | `pkg/providers/anthropic` behind `feature_anthropic` |
| `google-generative-ai` | `google` Gemini API | Streams Gemini responses, tool calls, images and thinking controls with `GEMINI_API_KEY`. | preserve | `packages/ai/src/providers/google.ts`, `packages/ai/src/providers/google-shared.ts`, `packages/ai/test/google-*.test.ts` | `pkg/providers/google` behind `feature_google` |
| `google-gemini-cli` | `google-gemini-cli`, Google Cloud Code Assist | Requires OAuth; handles Cloud Code Assist headers/endpoints, retry delay and empty stream quirks. | preserve | `packages/ai/src/providers/google-gemini-cli.ts`, `packages/ai/src/utils/oauth/google-gemini-cli.ts`, `packages/ai/test/google-gemini-cli-*.test.ts` | `pkg/providers/google` optional auth variant |
| `google-vertex` | `google-vertex` | Uses API key or ADC plus project/location env vars, with Vertex endpoint handling. | preserve | `packages/ai/src/providers/google-vertex.ts`, `packages/ai/src/env-api-keys.ts`, `packages/ai/test/google-vertex-*.test.ts` | `pkg/providers/google/vertex` optional build tag or runtime feature |
| `azure-openai-responses` | `azure-openai-responses` | Responses-style Azure OpenAI endpoint, deployment/base URL/resource name/API version handling and Azure API key. | preserve | `packages/ai/src/providers/azure-openai-responses.ts`, `packages/ai/test/azure-openai-*.test.ts` | `pkg/providers/openai/azure` optional `feature_openai` subfeature |
| `bedrock-converse-stream` | `amazon-bedrock` | Uses AWS Bedrock converse stream, IAM/profile/bearer/container/web identity auth sources, model metadata and thinking payloads. | gap | `packages/ai/src/providers/amazon-bedrock.ts`, `packages/ai/src/bedrock-provider.ts`, `packages/ai/test/bedrock-*.test.ts` | `pkg/providers/bedrock` optional; review dependency and memory cost before standard build |
| `mistral-conversations` | `mistral` | Streams Mistral conversations, tool schema and reasoning mode compatibility with `MISTRAL_API_KEY`. | gap | `packages/ai/src/providers/mistral.ts`, `packages/ai/test/mistral-*.test.ts` | `pkg/providers/mistral` optional after core providers |

## Provider IDs and auth sources

| Provider ID | Current auth source(s) | Stream/API adapter | Status | Source in `pi-mono` | Proposed Go destination |
|---|---|---|---|---|---|
| `openai` | `OPENAI_API_KEY`, stored API key, runtime `--api-key` | `openai-responses` or `openai-completions` by model | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/openai` |
| `openai-codex` | OpenAI Codex/ChatGPT OAuth credentials | `openai-codex-responses` | preserve | `packages/ai/src/utils/oauth/openai-codex.ts`, `packages/ai/src/providers/openai-codex-responses.ts` | Optional OAuth in `pkg/providers/openai` |
| `anthropic` | `ANTHROPIC_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, stored API key/OAuth | `anthropic-messages` | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/utils/oauth/anthropic.ts`, `packages/ai/src/providers/anthropic.ts` | `pkg/providers/anthropic` |
| `github-copilot` | `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN`, OAuth | OpenAI/Anthropic-compatible routes through Copilot headers/policy | gap | `packages/ai/src/utils/oauth/github-copilot.ts`, `packages/ai/src/providers/github-copilot-headers.ts`, `packages/ai/test/github-copilot-*.test.ts` | Optional compatible provider/auth extension |
| `google` | `GEMINI_API_KEY` | `google-generative-ai` | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/providers/google.ts` | `pkg/providers/google` |
| `google-gemini-cli` | Google Cloud Code Assist OAuth | `google-gemini-cli` | preserve | `packages/ai/src/utils/oauth/google-gemini-cli.ts`, `packages/ai/src/providers/google-gemini-cli.ts` | `pkg/providers/google` OAuth variant |
| `google-antigravity` | Antigravity OAuth and Cloud Code Assist endpoints | Gemini CLI / compatible endpoint behavior | gap | `packages/ai/src/utils/oauth/google-antigravity.ts`, `packages/ai/src/providers/google-gemini-cli.ts` | Optional after primary Google provider |
| `google-vertex` | `GOOGLE_CLOUD_API_KEY` or ADC + `GOOGLE_CLOUD_PROJECT`/`GCLOUD_PROJECT` + `GOOGLE_CLOUD_LOCATION` | `google-vertex` | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/providers/google-vertex.ts` | `pkg/providers/google/vertex` |
| `azure-openai-responses` | `AZURE_OPENAI_API_KEY` plus Azure base/resource/deployment/API version config | `azure-openai-responses` | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/providers/azure-openai-responses.ts` | `pkg/providers/openai/azure` |
| `amazon-bedrock` | `AWS_PROFILE`, IAM keys, bearer token, ECS/IRSA sources and region | `bedrock-converse-stream` | gap | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/providers/amazon-bedrock.ts` | Optional `pkg/providers/bedrock` |
| `deepseek` | `DEEPSEEK_API_KEY` | `openai-completions` compatible | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/compatible` |
| `groq` | `GROQ_API_KEY` | `openai-completions` compatible | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/compatible` |
| `cerebras` | `CEREBRAS_API_KEY` | `openai-completions` compatible | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/compatible` |
| `xai` | `XAI_API_KEY` | `openai-completions` compatible | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/compatible` |
| `openrouter` | `OPENROUTER_API_KEY` | `openai-completions` compatible with routing and cache compatibility | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/types.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/compatible` |
| `vercel-ai-gateway` | `AI_GATEWAY_API_KEY` | `openai-completions` compatible with gateway routing | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/types.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/compatible` |
| `zai` | `ZAI_API_KEY` | `openai-completions` compatible with thinking/tool stream quirks | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/types.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/compatible` |
| `mistral` | `MISTRAL_API_KEY` | `mistral-conversations` | gap | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/providers/mistral.ts` | Optional `pkg/providers/mistral` |
| `minimax` | `MINIMAX_API_KEY` | compatible model registry entry | gap | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | Later compatible-provider validation |
| `minimax-cn` | `MINIMAX_CN_API_KEY` | compatible model registry entry | gap | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | Later compatible-provider validation |
| `huggingface` | `HF_TOKEN` | compatible model registry entry | gap | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | Later compatible-provider validation |
| `fireworks` | `FIREWORKS_API_KEY` | compatible model registry entry | preserve | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | `pkg/providers/compatible` |
| `opencode`, `opencode-go` | `OPENCODE_API_KEY` | compatible model registry entry | gap | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | Later compatible-provider validation |
| `kimi-coding` | `KIMI_API_KEY` | compatible model registry entry | gap | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts` | Later compatible-provider validation |
| `cloudflare-workers-ai` | `CLOUDFLARE_API_KEY` plus account ID | compatible/custom endpoint behavior | gap | `packages/ai/src/env-api-keys.ts`, `packages/ai/src/models.generated.ts`, `packages/coding-agent/src/cli/args.ts` | Later compatible-provider validation |

## Provider-specific verification rows

| Verification item | Required Go parity | Status | Source in `pi-mono` | Proposed Go test location |
|---|---|---|---|---|
| OpenAI Responses SSE | Text deltas, reasoning replay, response ID, tool result images, partial JSON cleanup and cache affinity. | preserve | `packages/ai/test/openai-responses-*.test.ts` | `pkg/providers/openai` fake SSE tests |
| OpenAI-compatible Chat Completions | Tool choice, prompt cache, thinking-as-text, tool result images, response model, empty tools and provider compat flags. | preserve | `packages/ai/test/openai-completions-*.test.ts`, `packages/ai/test/openrouter-cache-write-repro.test.ts` | `pkg/providers/compatible` fake HTTP/SSE tests |
| Anthropic | SSE parsing, OAuth, eager tool input compatibility, thinking disable, cache retention and tool name normalization. | preserve | `packages/ai/test/anthropic-*.test.ts` | `pkg/providers/anthropic` fake SSE/OAuth tests |
| Google | Tool call IDs, thinking signatures, image tool result routing, missing args, Vertex API key resolution and Gemini CLI retry delays. | preserve | `packages/ai/test/google-*.test.ts` | `pkg/providers/google` fake server tests |
| Bedrock | Endpoint resolution, model metadata and thinking payload compatibility. | gap | `packages/ai/test/bedrock-*.test.ts` | Optional `pkg/providers/bedrock` tests |
| Generic stream behavior | Abort, empty stream, total tokens, unicode surrogate sanitization, overflow and tool call ID normalization. | preserve | `packages/ai/test/abort.test.ts`, `stream.test.ts`, `tokens.test.ts`, `unicode-surrogate.test.ts`, `overflow.test.ts`, `tool-call-id-normalization.test.ts` | `pkg/ai`, `pkg/providers` shared tests |

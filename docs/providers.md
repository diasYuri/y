# Providers

Activity: `phase-2-provider-matrix`

The Go runtime ships provider implementations as native packages. They use
streaming HTTP APIs and normalize provider output into `pkg/ai` events:

- text deltas become `ai.TextDelta`
- tool calls become `ai.ToolCallEvent`
- token accounting becomes `ai.UsageEvent`
- terminal conditions become `ai.StopEvent`
- provider stream errors become `ai.ErrorEvent`

## Anthropic

Package: `pkg/providers/anthropic`

Default API: `https://api.anthropic.com/v1/messages`

Auth priority:

1. `providers.StreamOptions.APIKey`
2. `anthropic.WithAPIKey`
3. `ANTHROPIC_OAUTH_TOKEN`
4. `ANTHROPIC_API_KEY`

When `ANTHROPIC_OAUTH_TOKEN` is used, the provider sends an `Authorization:
Bearer ...` header. Otherwise it sends `X-API-Key`. The provider always sends
`Anthropic-Version: 2023-06-01`.

## Google Gemini

Package: `pkg/providers/google`

Default API:
`https://generativelanguage.googleapis.com/v1beta/models/{model}:streamGenerateContent?alt=sse`

Auth priority:

1. `providers.StreamOptions.APIKey`
2. `google.WithAPIKey`
3. `GEMINI_API_KEY`

The API key is sent as both the `key` query parameter and `X-Goog-Api-Key`
header for compatibility with Gemini fake servers and gateways.

## OpenAI-Compatible

Package: `pkg/providers/openai_compatible`

Default API: `http://localhost:11434/v1/chat/completions`

Auth priority:

1. `providers.StreamOptions.APIKey`
2. `openai_compatible.WithAPIKey`
3. `OPENAI_COMPATIBLE_API_KEY`
4. `Y_OPENAI_COMPATIBLE_API_KEY`

The provider sends `Authorization: Bearer ...` when a key is present. Empty keys
are rejected by default. For local development endpoints that do not require
auth, set `Y_OPENAI_COMPATIBLE_ALLOW_EMPTY_KEY=true`.

Use `ai.Model.BaseURL` or the provider `WithBaseURL` option to route to local
servers or hosted OpenAI-compatible gateways.

## Current Scope

This phase covers the minimum native provider matrix. Stored auth, OAuth login
flows, model registry overrides, retry policy tuning, and provider-specific
compatibility flags are intentionally left for later phases unless required by a
consumer package.

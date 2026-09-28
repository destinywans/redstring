# Inference boundary

This package is an ADK v2.4.0 adapter, not a product session database. Every `Run`
creates an independent `session.InMemoryService()` and runner, reconstructing input
from the supplied frozen messages. Caller-owned PostgreSQL history remains the
source of truth. No global ADK app/user state is shared across attempts.

`Runner.Run(ctx, Request, emit)` returns the final answer only after the provider
terminates successfully. `delta` events are display fragments, not canonical
assistant messages. The caller commits the returned final answer and run stage in
its transaction. `evidence` callbacks must commit before returning; the tool does
not return evidence to the model until that callback succeeds. An emit failure
cancels the attempt and propagates the original error. No write tools are present.

Configured providers:

| Environment | Protocol | Default base URL |
| --- | --- | --- |
| `OLLAMA_MODEL` | Native Ollama `/api/chat` NDJSON stream | `http://localhost:11434` |
| `OPENAI_MODEL` | OpenAI Chat Completions SSE | `https://api.openai.com/v1` |
| `CLAUDE_MODEL` | Claude Messages streaming | `https://api.anthropic.com/v1` |

Each model variable accepts comma-separated names. `*_BASE_URL` and `*_API_KEY`
override the endpoint and credential. `*_API` explicitly selects the wire
protocol: OpenAI `chat_completions` (default) or `responses`, Claude `messages`,
Ollama `ollama` (default) or `chat_completions`. Native Ollama requires the server
root; Ollama compatibility mode defaults to `/v1` when no base URL is specified.
Public model IDs are `provider:model`.
An empty configuration exposes no models; there is no fake response fallback.
`Tools` and `Streaming` describe adapter support, not a successful remote health
or model-capability check. Models that require other endpoint/protocol semantics
need a separate adapter; an arbitrary "OpenAI-compatible" server is not guaranteed
to implement tool streaming correctly. `*_REASONING_KIND` and
`*_REASONING_LEVELS` advertise only configured model capabilities. Supported
profiles are `openai_effort`, `claude_adaptive`, and native `ollama_think` with
`on,off`; no selectable levels are exposed by default. The adapter maps effort
to `reasoning.effort` for Responses, `reasoning_effort` for Chat Completions,
Claude adaptive thinking, or Ollama `think`. Unsupported choices fail before
the provider request. Responses calls send `store: false` and reconstruct tool
turns from the caller-owned transcript.

The bounded defaults are 5 minutes per attempt, 8 model calls, 32 evidence reads,
16 registered tools, 2 MiB frozen input, 8 KiB tool query, 256 KiB evidence content,
8 MiB response stream, and 4096 output tokens per model call. Provider calls are
not silently retried. A product retry creates a new attempt and may incur another
charge. The defaults can be lowered with `AIOPS_INFERENCE_MAX_CALLS` (1–8 calls
per run) and
`OPENAI_MAX_OUTPUT_TOKENS` (1–4096 tokens per Chat Completions or Responses
request). These settings cannot raise the existing defaults.
Hidden reasoning and provider-internal session IDs are not persisted.

DeepSeek Flash uses the OpenAI-compatible Chat Completions adapter with
`OPENAI_MODEL=deepseek-flash`, `OPENAI_BASE_URL=https://api.deepseek.com`, and
`OPENAI_PROVIDER_NAME=DeepSeek` for the V3 catalog label. Keep its key in the
local `OPENAI_API_KEY` environment, never in tracked configuration. For a
bounded connection check, set `AIOPS_INFERENCE_MAX_CALLS=1` and
`OPENAI_MAX_OUTPUT_TOKENS=128`. A request can still incur provider charges;
these caps limit this process, not the provider account.

Embedding uses `EMBEDDING_PROVIDER`, `EMBEDDING_MODEL`, `EMBEDDING_BASE_URL`, and
`EMBEDDING_API_KEY`; default is local Ollama `bge-m3` via `/api/embed`. OpenAI uses
`/embeddings`. The caller owns index generation and records actual returned vector
dimension with provider/model identity. Inputs reject silent Ollama truncation.
Batches are bounded to 128 texts / 2 MiB and checked for missing, duplicate,
inconsistent, or non-finite vectors.

Protocol tests use local `httptest` endpoints with real ADK execution. They cover
tool result ordering, callback persistence failure, branch isolation, cancellation,
call limits, fragmented tool arguments, incomplete streams, Responses and native
Ollama tool round-trips, and embeddings.
`AIOPS_REAL_OLLAMA=1 go test -run TestOllamaRealSmoke -v ./internal/inference`
explicitly invokes local qwen3.6 and bge-m3. This opt-in smoke is separate from
cloud credential acceptance and real infrastructure evidence integration.

Sources checked during implementation:

- https://github.com/google/adk-go/blob/v2.4.0/runner/runner.go
- https://github.com/google/adk-go/blob/v2.4.0/model/llm.go
- https://github.com/google/adk-go/blob/v2.4.0/tool/functiontool/function.go
- https://docs.ollama.com/api/openai-compatibility
- https://docs.ollama.com/api/chat
- https://docs.ollama.com/capabilities/tool-calling
- https://platform.openai.com/docs/api-reference/responses-streaming
- https://platform.claude.com/docs/en/build-with-claude/streaming
- https://developers.openai.com/api/reference/resources/chat

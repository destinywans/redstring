-- Executable configuration is fixed for an Agent version. Changing it requires
-- a new version and normal evaluation/release decisions.
CREATE TABLE v3_agent_execution_configs (
 agent_id text NOT NULL,
 version text NOT NULL,
 model_id text NOT NULL,
 tool_names text[] NOT NULL DEFAULT '{}',
 prompt_id text NOT NULL,
 prompt_version integer NOT NULL,
 max_model_calls integer NOT NULL CHECK (max_model_calls BETWEEN 1 AND 5),
 max_tool_calls integer NOT NULL CHECK (max_tool_calls BETWEEN 0 AND 4),
 max_input_bytes integer NOT NULL CHECK (max_input_bytes BETWEEN 1 AND 8192),
 max_output_bytes integer NOT NULL CHECK (max_output_bytes BETWEEN 1 AND 32768),
 max_output_tokens integer NOT NULL CHECK (max_output_tokens BETWEEN 1 AND 4096),
 timeout_seconds integer NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 90),
 config_sha256 text NOT NULL CHECK (config_sha256 ~ '^[a-f0-9]{64}$'),
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(agent_id,version),
 FOREIGN KEY(agent_id,version) REFERENCES v3_agent_versions(agent_id,version),
 FOREIGN KEY(prompt_id,prompt_version) REFERENCES v3_agent_prompt_versions(prompt_id,version)
);

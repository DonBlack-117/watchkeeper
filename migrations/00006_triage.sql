-- +goose Up
-- One AI investigation of an incident. An incident can be investigated again
-- (by hand from the dashboard); the newest run is the one shown.
CREATE TABLE incident_triages (
  id BIGSERIAL PRIMARY KEY,
  incident_id BIGINT NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('running', 'done', 'failed')),
  -- What started it: 'auto' when the incident went down, 'manual' from the API.
  trigger TEXT NOT NULL CHECK (trigger IN ('auto', 'manual')),
  model TEXT NOT NULL CHECK (LENGTH(model) <= 100),
  summary TEXT CHECK (LENGTH(summary) <= 2000),
  probable_cause TEXT CHECK (LENGTH(probable_cause) <= 2000),
  -- 'low', 'medium' or 'high', as the model rated its own diagnosis.
  confidence TEXT CHECK (confidence IN ('low', 'medium', 'high')),
  -- JSON arrays of short strings.
  evidence JSONB NOT NULL DEFAULT '[]',
  next_steps JSONB NOT NULL DEFAULT '[]',
  -- The tools the agent called, in order, for the timeline.
  tool_calls JSONB NOT NULL DEFAULT '[]',
  input_tokens INT NOT NULL DEFAULT 0,
  output_tokens INT NOT NULL DEFAULT 0,
  error TEXT CHECK (LENGTH(error) <= 1000),
  started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  finished_at TIMESTAMPTZ
);

-- At most one investigation running per incident.
CREATE UNIQUE INDEX incident_triages_one_running ON incident_triages (incident_id)
WHERE
  state = 'running';

CREATE INDEX incident_triages_incident ON incident_triages (incident_id, started_at DESC);

-- The daily limit counts runs by start time.
CREATE INDEX incident_triages_started ON incident_triages (started_at);

-- +goose Down
DROP TABLE incident_triages;

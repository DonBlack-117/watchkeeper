-- +goose Up
-- An incident spans from the first run where a check stops being operational
-- until it is operational again. At most one is open per check.
CREATE TABLE incidents (
  id BIGSERIAL PRIMARY KEY,
  check_id BIGINT NOT NULL REFERENCES checks (id) ON DELETE CASCADE,
  status TEXT NOT NULL CHECK (status IN ('degraded', 'down')),
  started_at TIMESTAMPTZ NOT NULL,
  resolved_at TIMESTAMPTZ,
  -- Whether an "opened" alert went out, so "resolved" is only sent after one.
  notified BOOLEAN NOT NULL DEFAULT FALSE,
  CHECK (resolved_at IS NULL OR resolved_at >= started_at)
);

CREATE UNIQUE INDEX incidents_one_open ON incidents (check_id) WHERE resolved_at IS NULL;

CREATE INDEX incidents_started ON incidents (started_at DESC);

-- The timeline shown on the status page: opened, escalated, improved, resolved.
CREATE TABLE incident_updates (
  id BIGSERIAL PRIMARY KEY,
  incident_id BIGINT NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
  at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('degraded', 'down', 'resolved')),
  -- Short error category from the run (timeout, dns: no such host); never shown publicly.
  error TEXT CHECK (LENGTH(error) <= 1000)
);

CREATE INDEX incident_updates_incident ON incident_updates (incident_id, at);

-- One row per certificate and warning threshold, so each warning is sent once,
-- survives restarts and starts over when the certificate is renewed.
CREATE TABLE cert_notices (
  check_id BIGINT NOT NULL REFERENCES checks (id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  threshold_days INT NOT NULL CHECK (threshold_days > 0),
  sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (check_id, expires_at, threshold_days)
);

-- +goose Down
DROP TABLE cert_notices;

DROP TABLE incident_updates;

DROP TABLE incidents;

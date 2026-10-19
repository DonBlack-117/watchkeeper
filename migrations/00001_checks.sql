-- +goose Up
CREATE TABLE checks (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL CHECK (LENGTH(name) BETWEEN 1 AND 100),
  kind TEXT NOT NULL CHECK (kind IN ('http', 'dns', 'tls')),
  target TEXT NOT NULL,
  interval_s INT NOT NULL DEFAULT 60 CHECK (interval_s BETWEEN 30 AND 86400),
  timeout_ms INT NOT NULL DEFAULT 10000 CHECK (timeout_ms BETWEEN 1000 AND 30000),
  expected_status INT CHECK (expected_status BETWEEN 100 AND 599),
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE checks;

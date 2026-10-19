-- +goose Up
CREATE TABLE check_results (
  id BIGSERIAL PRIMARY KEY,
  check_id BIGINT NOT NULL REFERENCES checks (id) ON DELETE CASCADE,
  started_at TIMESTAMPTZ NOT NULL,
  ok BOOLEAN NOT NULL,
  status_code INT,
  total_ms INT NOT NULL,
  dns_ms INT,
  connect_ms INT,
  tls_ms INT,
  ttfb_ms INT,
  cert_expires_at TIMESTAMPTZ,
  error TEXT
);

CREATE INDEX check_results_check_time ON check_results (check_id, started_at DESC);

-- +goose Down
DROP TABLE check_results;

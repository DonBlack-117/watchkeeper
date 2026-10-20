-- +goose Up
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL UNIQUE CHECK (LENGTH(username) BETWEEN 1 AND 64),
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Only the SHA-256 of the session token is stored, so a database leak does not leak sessions.
CREATE TABLE sessions (
  token_hash BYTEA PRIMARY KEY CHECK (LENGTH(token_hash) = 32),
  user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE INDEX sessions_user ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;

DROP TABLE users;

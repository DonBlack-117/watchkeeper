-- +goose Up
-- Covers DailyUptime and RecentResults (index-only on ok) and keyset pagination by id.
CREATE INDEX check_results_check_time_ok ON check_results (check_id, started_at DESC) INCLUDE (ok);

DROP INDEX check_results_check_time;

CREATE INDEX check_results_check_id_desc ON check_results (check_id, id DESC);

-- Covers UptimeSince and the retention delete.
CREATE INDEX check_results_started ON check_results (started_at) INCLUDE (check_id, ok);

ALTER TABLE check_results ADD CONSTRAINT check_results_total_ms_nonneg CHECK (total_ms >= 0), ADD CONSTRAINT check_results_status_code_range CHECK (status_code BETWEEN 100 AND 599), ADD CONSTRAINT check_results_error_len CHECK (LENGTH(error) <= 1000);

ALTER TABLE checks ADD CONSTRAINT checks_target_len CHECK (LENGTH(target) <= 2048);

-- +goose Down
ALTER TABLE checks DROP CONSTRAINT checks_target_len;

ALTER TABLE check_results DROP CONSTRAINT check_results_error_len, DROP CONSTRAINT check_results_status_code_range, DROP CONSTRAINT check_results_total_ms_nonneg;

DROP INDEX check_results_started;

DROP INDEX check_results_check_id_desc;

CREATE INDEX check_results_check_time ON check_results (check_id, started_at DESC);

DROP INDEX check_results_check_time_ok;

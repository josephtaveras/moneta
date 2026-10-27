-- Record the connection warnings each import run carried, so sync can start
-- its overlap from the last checkpoint committed without warnings.
ALTER TABLE import_runs
    ADD COLUMN connection_warnings INTEGER NOT NULL DEFAULT 0
        CHECK (connection_warnings >= 0);

-- SimpleFIN sync never runs recurring detection. Detector state left by
-- earlier Plaid detection would keep stale detected rows visible in bills,
-- recurring, and the dashboard, so return it to the seeded never_run state.
-- Detected rows stay stored but hidden until detection runs again.
UPDATE detector_state
SET status = 'never_run',
    last_run_at = NULL,
    last_success_at = NULL,
    last_error = '',
    last_series_count = 0,
    last_skipped_overflow = 0
WHERE id = 1;

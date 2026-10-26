-- The detector_state reset is intentionally not reversed: the earlier status,
-- timestamps, and counts were not retained, and never_run is valid for the
-- previous schema too.
ALTER TABLE import_runs
    DROP COLUMN connection_warnings;

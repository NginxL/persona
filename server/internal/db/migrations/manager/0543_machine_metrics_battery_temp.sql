-- On-demand battery percentage and host/SoC temperature samples (nullable; omitted when unavailable).
ALTER TABLE machine_metrics
    ADD COLUMN IF NOT EXISTS battery_percent real,
    ADD COLUMN IF NOT EXISTS temp_c          real;

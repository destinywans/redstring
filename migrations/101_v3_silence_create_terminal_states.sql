-- A delayed intent must not be sent with a materially shortened window.
-- Ambiguous upstream duplicates or payload drift require human resolution.
ALTER TABLE v3_silence_create_operations
 DROP CONSTRAINT v3_silence_create_operations_state_check;
ALTER TABLE v3_silence_create_operations
 ADD CONSTRAINT v3_silence_create_operations_state_check
 CHECK (state IN ('reserved','dispatching','unknown','acknowledged','confirmed','manual_review','expired'));

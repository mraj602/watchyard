-- Version 20: seat.pick_mode and seat.pick_mode_src (setup.md 8.5, Kind): how
-- `wy account next` should use the seat, the same on every machine, and who set it ('ui':
-- PATCH /v1/accounts/{seat}; 'slot': a slot_info `mode`, applied only while pick_mode is NULL).

ALTER TABLE seat ADD COLUMN pick_mode TEXT CHECK (pick_mode IS NULL OR pick_mode IN ('protected', 'aggressive'));
ALTER TABLE seat ADD COLUMN pick_mode_src TEXT CHECK (pick_mode_src IS NULL OR pick_mode_src IN ('ui', 'slot'));

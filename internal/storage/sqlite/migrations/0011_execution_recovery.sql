-- Empty reference identifies older evidence whose branch was never captured.
ALTER TABLE execution_runs ADD COLUMN starting_ref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(starting_ref AS BLOB)) <= 4096);
ALTER TABLE execution_runs ADD COLUMN schedule_mode TEXT NOT NULL DEFAULT 'run_one' CHECK (schedule_mode IN ('run_one','drain','watch'));
ALTER TABLE execution_runs ADD COLUMN schedule_remaining INTEGER NOT NULL DEFAULT 1 CHECK (schedule_remaining BETWEEN 1 AND 10000);
-- Older rows have no remaining-limit evidence. Keep the conservative one-unit
-- default; never infer a renewed Watch/Drain authorization from old mode alone.

ALTER TABLE schedule_shift DROP CONSTRAINT IF EXISTS schedule_shift_required_headcount_check;
ALTER TABLE schedule_shift DROP COLUMN IF EXISTS required_headcount;

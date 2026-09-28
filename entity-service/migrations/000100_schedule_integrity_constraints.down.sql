ALTER TABLE schedule_shift DROP CONSTRAINT IF EXISTS schedule_shift_end_minute_check;
ALTER TABLE schedule_shift
    ADD CONSTRAINT schedule_shift_end_minute_check
    CHECK (end_minute > start_minute AND end_minute <= 2880);

DROP TRIGGER IF EXISTS schedule_assignment_matches_shift_trigger ON schedule_assignment;
DROP FUNCTION IF EXISTS schedule_assignment_matches_shift();

ALTER TABLE schedule_absence DROP CONSTRAINT IF EXISTS schedule_absence_no_overlap;
ALTER TABLE schedule_assignment DROP CONSTRAINT IF EXISTS schedule_assignment_no_overlap;

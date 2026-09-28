-- How many people a window is meant to have on it.
--
-- Without this the rota can say who is on but never that a slot is short, which
-- is the one thing editing introduces the risk of: a lead removes somebody at
-- 16:00 and nothing anywhere notices the evening is down to six of seven.
--
-- Nullable on purpose, and null is not zero. It means "no target defined for
-- this window", which is the honest state for a window whose headcount is not a
-- rota rule -- see CRE_AMERICAS below. A view must treat null as "cannot say"
-- rather than as "nobody needed".
ALTER TABLE schedule_shift
    ADD COLUMN IF NOT EXISTS required_headcount SMALLINT;

ALTER TABLE schedule_shift
    DROP CONSTRAINT IF EXISTS schedule_shift_required_headcount_check;
ALTER TABLE schedule_shift
    ADD CONSTRAINT schedule_shift_required_headcount_check
    CHECK (required_headcount IS NULL OR required_headcount > 0);

-- The CRE targets, as the rota is actually run.
UPDATE schedule_shift SET required_headcount = 7 WHERE code = 'CRE_EVENING';       -- one per ABT
UPDATE schedule_shift SET required_headcount = 1 WHERE code = 'CRE_MORNING';
UPDATE schedule_shift SET required_headcount = 1 WHERE code = 'CRE_MORNING_OC';
UPDATE schedule_shift SET required_headcount = 3 WHERE code = 'CRE_WEEKEND';       -- three ABT engineers

-- Deliberately left null:
--
--   CRE_AMERICAS  carries two different meanings on one code. On a weekday it is
--                 simply when that team works, so there is no target -- the whole
--                 team is on it. At a weekend it is a rota of one. A single
--                 column cannot say both, and guessing either way would make the
--                 gap indicator wrong five days out of seven. Splitting the
--                 weekend cover into its own shift code would fix it, and is a
--                 change worth making deliberately rather than as a side effect
--                 of adding this column.
--
--   SRE_*         the zone windows want targets too, but the exact per-tier
--                 numbers are a question for the SRE leads rather than something
--                 to infer from the seed. Null until they say.
COMMENT ON COLUMN schedule_shift.required_headcount IS
  'Target number of engineers for this window; NULL means no target is defined, which is not the same as zero.';

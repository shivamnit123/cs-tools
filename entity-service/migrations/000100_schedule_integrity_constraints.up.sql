-- Constraints the rota's own invariants were relying on the application to
-- keep. Raised in ERD review: each of these is something the schema allowed
-- and nothing rejected, and each produces a silently wrong answer rather than
-- an error when it happens.

-- Needed for the two EXCLUDE constraints below: they mix an equality test on a
-- uuid with an overlap test on a range, and a plain GiST opclass has no
-- equality operator for uuid.
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- ── One person cannot be in two places at once ────────────────────────────
--
-- schedule_assignment_unique_slot only blocked the same shift twice for a
-- person on a day. It said nothing about two *different* windows whose hours
-- physically overlap -- an SRE engineer on TZ1 06:00-13:30 and TZ2 07:00-21:00
-- on the same day passed it. The whole point of the tstzrange index is "who is
-- on duty right now", and that question had two contradictory answers with
-- nothing having refused the insert.
--
-- Over the resolved instants rather than rota_date, because that is what
-- "overlap" means here: a night block belongs to the day it started on but
-- runs into the next, and two blocks either share wall-clock time or they do
-- not. Half-open at the end, so a window ending 18:00 and one starting 18:00
-- are a handover, not a clash.
ALTER TABLE schedule_assignment
    ADD CONSTRAINT schedule_assignment_no_overlap
    EXCLUDE USING gist (
        user_id WITH =,
        tstzrange(starts_at, ends_at, '[)') WITH &&
    );

-- ── Nor away twice for two different reasons ──────────────────────────────
--
-- Two absence rows for one person could overlap freely -- annual leave and a
-- customer allocation covering the same Tuesday -- and "why is this person
-- off today" had no defined tiebreak. Closed at both ends, because a leave
-- day is a whole day: 1st-3rd and 3rd-5th is a real clash, not a handover.
ALTER TABLE schedule_absence
    ADD CONSTRAINT schedule_absence_no_overlap
    EXCLUDE USING gist (
        user_id WITH =,
        daterange(starts_on, ends_on, '[]') WITH &&
    );

-- ── An assignment may specialise its window, never contradict it ──────────
--
-- zone_id and tier are stored on the assignment rather than read through
-- shift_id, and idx_schedule_assignment_zone_tier_date reads them directly --
-- so a row claiming shift SRE_TZ1 with zone TZ2 gives a wrong answer to "who
-- is L2 in TZ2" with no join to catch it.
--
-- They cannot simply be dropped in favour of the shift's own values: the
-- catalogue deliberately leaves both open in places. SRE_REGULAR fixes no
-- zone, because regular hours are worked in every zone and the assignment
-- says which; SRE_TZ1 fixes a zone but no tier, because which of L1 or L2
-- somebody is that week is a fact about the person, not the window.
--
-- So the rule is the narrower one: where the shift fixes a value the
-- assignment must match it, and where the shift leaves it open the assignment
-- may fill it in. A CHECK cannot reach another table, so this is a trigger.
CREATE OR REPLACE FUNCTION schedule_assignment_matches_shift()
RETURNS TRIGGER AS $$
DECLARE
    shift_zone UUID;
    shift_tier schedule_tier_enum;
    shift_code TEXT;
BEGIN
    SELECT s.zone_id, s.tier, s.code INTO shift_zone, shift_tier, shift_code
      FROM schedule_shift s WHERE s.id = NEW.shift_id;

    IF shift_zone IS NOT NULL AND NEW.zone_id IS DISTINCT FROM shift_zone THEN
        RAISE EXCEPTION
            'assignment zone does not match shift % (shift fixes zone %, assignment says %)',
            shift_code, shift_zone, NEW.zone_id
            USING ERRCODE = 'check_violation';
    END IF;

    IF shift_tier IS NOT NULL AND NEW.tier IS DISTINCT FROM shift_tier THEN
        RAISE EXCEPTION
            'assignment tier does not match shift % (shift fixes tier %, assignment says %)',
            shift_code, shift_tier, NEW.tier
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER schedule_assignment_matches_shift_trigger
    BEFORE INSERT OR UPDATE OF shift_id, zone_id, tier ON schedule_assignment
    FOR EACH ROW EXECUTE FUNCTION schedule_assignment_matches_shift();

-- ── A window carries at most one day forward ──────────────────────────────
--
-- end_minute <= 2880 allowed 48 hours, while every documented window carries
-- at most one midnight: > 1440 is the next day, and crosses_midnight is
-- generated from exactly that. The old bound would have accepted a typo like
-- 2000 on a new catalogue row instead of refusing it.
ALTER TABLE schedule_shift DROP CONSTRAINT schedule_shift_end_minute_check;
ALTER TABLE schedule_shift
    ADD CONSTRAINT schedule_shift_end_minute_check
    CHECK (end_minute > start_minute AND end_minute <= start_minute + 1440);

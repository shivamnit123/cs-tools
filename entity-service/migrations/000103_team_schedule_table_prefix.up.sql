-- Everything this feature owns gains a team_schedule_ prefix.
--
-- "schedule" on its own is an overloaded word in a platform that also runs
-- scheduled tasks, and a table called schedule_assignment sitting beside them
-- invites exactly the wrong guess about what it holds. Raised in review, and
-- done now: these tables are not released yet, so a rename costs one migration
-- today and a great deal more once anything outside this repository reads them.
--
-- Renames only. No column, constraint or index changes anything it enforces --
-- only what it is called -- so this is safe to apply to a populated database
-- and needs no backfill. Earlier migrations keep the old names on purpose:
-- they run before this one, and rewriting applied history is how a migration
-- gets re-applied from scratch against a live database.

-- ── tables ────────────────────────────────────────────────────────────────
ALTER TABLE schedule_absence RENAME TO team_schedule_absence;
ALTER TABLE schedule_absence_activity RENAME TO team_schedule_absence_activity;
ALTER TABLE schedule_absence_kind RENAME TO team_schedule_absence_kind;
ALTER TABLE schedule_assignment RENAME TO team_schedule_assignment;
ALTER TABLE schedule_assignment_activity RENAME TO team_schedule_assignment_activity;
ALTER TABLE schedule_shift RENAME TO team_schedule_shift;
ALTER TABLE schedule_zone RENAME TO team_schedule_zone;

-- ── enums ─────────────────────────────────────────────────────────────────
ALTER TYPE schedule_absence_bucket_enum RENAME TO team_schedule_absence_bucket_enum;
ALTER TYPE schedule_day_scope_enum RENAME TO team_schedule_day_scope_enum;
ALTER TYPE schedule_shift_family_enum RENAME TO team_schedule_shift_family_enum;
ALTER TYPE schedule_source_enum RENAME TO team_schedule_source_enum;
ALTER TYPE schedule_tier_enum RENAME TO team_schedule_tier_enum;

-- ── the zone/tier agreement trigger and its function ──────────────────────
ALTER TRIGGER schedule_assignment_matches_shift_trigger ON team_schedule_assignment
    RENAME TO team_schedule_assignment_matches_shift_trigger;
ALTER FUNCTION schedule_assignment_matches_shift()
    RENAME TO team_schedule_assignment_matches_shift;

-- A plpgsql body is text, resolved when it runs, so renaming the type and the
-- table it names left the function still looking for schedule_tier_enum and
-- schedule_shift. Replaced rather than renamed for that reason.
CREATE OR REPLACE FUNCTION team_schedule_assignment_matches_shift()
RETURNS TRIGGER AS $$
DECLARE
    shift_zone UUID;
    shift_tier team_schedule_tier_enum;
    shift_code TEXT;
BEGIN
    SELECT s.zone_id, s.tier, s.code INTO shift_zone, shift_tier, shift_code
      FROM team_schedule_shift s WHERE s.id = NEW.shift_id;

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

-- ── indexes ───────────────────────────────────────────────────────────────
ALTER INDEX idx_schedule_absence_activity_absence RENAME TO idx_team_schedule_absence_activity_absence;
ALTER INDEX idx_schedule_absence_activity_team RENAME TO idx_team_schedule_absence_activity_team;
ALTER INDEX idx_schedule_absence_activity_user RENAME TO idx_team_schedule_absence_activity_user;
ALTER INDEX idx_schedule_absence_kind_id RENAME TO idx_team_schedule_absence_kind_id;
ALTER INDEX idx_schedule_absence_span RENAME TO idx_team_schedule_absence_span;
ALTER INDEX idx_schedule_absence_team RENAME TO idx_team_schedule_absence_team;
ALTER INDEX idx_schedule_absence_user RENAME TO idx_team_schedule_absence_user;
ALTER INDEX idx_schedule_assignment_activity_assignment RENAME TO idx_team_schedule_assignment_activity_assignment;
ALTER INDEX idx_schedule_assignment_activity_team_date RENAME TO idx_team_schedule_assignment_activity_team_date;
ALTER INDEX idx_schedule_assignment_activity_user_date RENAME TO idx_team_schedule_assignment_activity_user_date;
ALTER INDEX idx_schedule_assignment_rota_date RENAME TO idx_team_schedule_assignment_rota_date;
ALTER INDEX idx_schedule_assignment_team_date RENAME TO idx_team_schedule_assignment_team_date;
ALTER INDEX idx_schedule_assignment_user_date RENAME TO idx_team_schedule_assignment_user_date;
ALTER INDEX idx_schedule_assignment_window RENAME TO idx_team_schedule_assignment_window;
ALTER INDEX idx_schedule_assignment_zone_tier_date RENAME TO idx_team_schedule_assignment_zone_tier_date;
ALTER INDEX idx_schedule_shift_family RENAME TO idx_team_schedule_shift_family;
ALTER INDEX idx_schedule_shift_zone_id RENAME TO idx_team_schedule_shift_zone_id;

-- ── constraints ───────────────────────────────────────────────────────────
ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_kind_id_fkey TO team_schedule_absence_kind_id_fkey;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_no_overlap TO team_schedule_absence_no_overlap;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_pkey TO team_schedule_absence_pkey;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_range_check TO team_schedule_absence_range_check;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_team_key_fkey TO team_schedule_absence_team_key_fkey;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_user_id_fkey TO team_schedule_absence_user_id_fkey;
ALTER TABLE team_schedule_absence_activity RENAME CONSTRAINT schedule_absence_activity_action_check TO team_schedule_absence_activity_action_check;
ALTER TABLE team_schedule_absence_activity RENAME CONSTRAINT schedule_absence_activity_pkey TO team_schedule_absence_activity_pkey;
ALTER TABLE team_schedule_absence_kind RENAME CONSTRAINT schedule_absence_kind_code_key TO team_schedule_absence_kind_code_key;
ALTER TABLE team_schedule_absence_kind RENAME CONSTRAINT schedule_absence_kind_pkey TO team_schedule_absence_kind_pkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_no_overlap TO team_schedule_assignment_no_overlap;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_pkey TO team_schedule_assignment_pkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_shift_id_fkey TO team_schedule_assignment_shift_id_fkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_team_agrees TO team_schedule_assignment_team_agrees;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_team_id_fkey TO team_schedule_assignment_team_id_fkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_team_key_fkey TO team_schedule_assignment_team_key_fkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_unique_slot TO team_schedule_assignment_unique_slot;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_user_id_fkey TO team_schedule_assignment_user_id_fkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_window_check TO team_schedule_assignment_window_check;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_zone_id_fkey TO team_schedule_assignment_zone_id_fkey;
ALTER TABLE team_schedule_assignment_activity RENAME CONSTRAINT schedule_assignment_activity_action_check TO team_schedule_assignment_activity_action_check;
ALTER TABLE team_schedule_assignment_activity RENAME CONSTRAINT schedule_assignment_activity_pkey TO team_schedule_assignment_activity_pkey;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_code_key TO team_schedule_shift_code_key;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_end_minute_check TO team_schedule_shift_end_minute_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_escalation_zone_check TO team_schedule_shift_escalation_zone_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_pkey TO team_schedule_shift_pkey;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_required_headcount_check TO team_schedule_shift_required_headcount_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_start_minute_check TO team_schedule_shift_start_minute_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_zone_family_check TO team_schedule_shift_zone_family_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_zone_id_fkey TO team_schedule_shift_zone_id_fkey;
ALTER TABLE team_schedule_zone RENAME CONSTRAINT schedule_zone_code_key TO team_schedule_zone_code_key;
ALTER TABLE team_schedule_zone RENAME CONSTRAINT schedule_zone_pkey TO team_schedule_zone_pkey;
ALTER TABLE team_schedule_zone RENAME CONSTRAINT schedule_zone_weekend_zone_id_fkey TO team_schedule_zone_weekend_zone_id_fkey;

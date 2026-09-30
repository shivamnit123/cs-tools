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
--
-- Every rename below is gated on one check, because a plain ALTER ... RENAME
-- is not idempotent and 0152's CREATE TABLE IF NOT EXISTS schedule_absence
-- cannot tell "never created" apart from "created, then renamed away by this
-- file" -- on a re-run against an already-migrated database it recreates the
-- old-named table from scratch, and an unguarded rename here would then
-- collide with the new name that already holds the real data. Same shape of
-- fix as 0091's control-plane table renames.
DO $$
BEGIN
    IF to_regclass('team_schedule_absence') IS NULL THEN
        -- ── tables ────────────────────────────────────────────────────────
        EXECUTE 'ALTER TABLE schedule_absence RENAME TO team_schedule_absence';
        EXECUTE 'ALTER TABLE schedule_absence_activity RENAME TO team_schedule_absence_activity';
        EXECUTE 'ALTER TABLE schedule_absence_kind RENAME TO team_schedule_absence_kind';
        EXECUTE 'ALTER TABLE schedule_assignment RENAME TO team_schedule_assignment';
        EXECUTE 'ALTER TABLE schedule_assignment_activity RENAME TO team_schedule_assignment_activity';
        EXECUTE 'ALTER TABLE schedule_shift RENAME TO team_schedule_shift';
        EXECUTE 'ALTER TABLE schedule_zone RENAME TO team_schedule_zone';

        -- ── enums ─────────────────────────────────────────────────────────
        EXECUTE 'ALTER TYPE schedule_absence_bucket_enum RENAME TO team_schedule_absence_bucket_enum';
        EXECUTE 'ALTER TYPE schedule_day_scope_enum RENAME TO team_schedule_day_scope_enum';
        EXECUTE 'ALTER TYPE schedule_shift_family_enum RENAME TO team_schedule_shift_family_enum';
        EXECUTE 'ALTER TYPE schedule_source_enum RENAME TO team_schedule_source_enum';
        EXECUTE 'ALTER TYPE schedule_tier_enum RENAME TO team_schedule_tier_enum';

        -- ── the zone/tier agreement trigger and its function ────────────────
        EXECUTE 'ALTER TRIGGER schedule_assignment_matches_shift_trigger ON team_schedule_assignment RENAME TO team_schedule_assignment_matches_shift_trigger';
        EXECUTE 'ALTER FUNCTION schedule_assignment_matches_shift() RENAME TO team_schedule_assignment_matches_shift';

        -- ── indexes ───────────────────────────────────────────────────────
        EXECUTE 'ALTER INDEX idx_schedule_absence_activity_absence RENAME TO idx_team_schedule_absence_activity_absence';
        EXECUTE 'ALTER INDEX idx_schedule_absence_activity_team RENAME TO idx_team_schedule_absence_activity_team';
        EXECUTE 'ALTER INDEX idx_schedule_absence_activity_user RENAME TO idx_team_schedule_absence_activity_user';
        EXECUTE 'ALTER INDEX idx_schedule_absence_kind_id RENAME TO idx_team_schedule_absence_kind_id';
        EXECUTE 'ALTER INDEX idx_schedule_absence_span RENAME TO idx_team_schedule_absence_span';
        EXECUTE 'ALTER INDEX idx_schedule_absence_team RENAME TO idx_team_schedule_absence_team';
        EXECUTE 'ALTER INDEX idx_schedule_absence_user RENAME TO idx_team_schedule_absence_user';
        EXECUTE 'ALTER INDEX idx_schedule_assignment_activity_assignment RENAME TO idx_team_schedule_assignment_activity_assignment';
        EXECUTE 'ALTER INDEX idx_schedule_assignment_activity_team_date RENAME TO idx_team_schedule_assignment_activity_team_date';
        EXECUTE 'ALTER INDEX idx_schedule_assignment_activity_user_date RENAME TO idx_team_schedule_assignment_activity_user_date';
        EXECUTE 'ALTER INDEX idx_schedule_assignment_rota_date RENAME TO idx_team_schedule_assignment_rota_date';
        EXECUTE 'ALTER INDEX idx_schedule_assignment_team_date RENAME TO idx_team_schedule_assignment_team_date';
        EXECUTE 'ALTER INDEX idx_schedule_assignment_user_date RENAME TO idx_team_schedule_assignment_user_date';
        EXECUTE 'ALTER INDEX idx_schedule_assignment_window RENAME TO idx_team_schedule_assignment_window';
        EXECUTE 'ALTER INDEX idx_schedule_assignment_zone_tier_date RENAME TO idx_team_schedule_assignment_zone_tier_date';
        EXECUTE 'ALTER INDEX idx_schedule_shift_family RENAME TO idx_team_schedule_shift_family';
        EXECUTE 'ALTER INDEX idx_schedule_shift_zone_id RENAME TO idx_team_schedule_shift_zone_id';

        -- ── constraints ───────────────────────────────────────────────────
        EXECUTE 'ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_kind_id_fkey TO team_schedule_absence_kind_id_fkey';
        EXECUTE 'ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_no_overlap TO team_schedule_absence_no_overlap';
        EXECUTE 'ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_pkey TO team_schedule_absence_pkey';
        EXECUTE 'ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_range_check TO team_schedule_absence_range_check';
        EXECUTE 'ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_team_key_fkey TO team_schedule_absence_team_key_fkey';
        EXECUTE 'ALTER TABLE team_schedule_absence RENAME CONSTRAINT schedule_absence_user_id_fkey TO team_schedule_absence_user_id_fkey';
        EXECUTE 'ALTER TABLE team_schedule_absence_activity RENAME CONSTRAINT schedule_absence_activity_action_check TO team_schedule_absence_activity_action_check';
        EXECUTE 'ALTER TABLE team_schedule_absence_activity RENAME CONSTRAINT schedule_absence_activity_pkey TO team_schedule_absence_activity_pkey';
        EXECUTE 'ALTER TABLE team_schedule_absence_kind RENAME CONSTRAINT schedule_absence_kind_code_key TO team_schedule_absence_kind_code_key';
        EXECUTE 'ALTER TABLE team_schedule_absence_kind RENAME CONSTRAINT schedule_absence_kind_pkey TO team_schedule_absence_kind_pkey';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_no_overlap TO team_schedule_assignment_no_overlap';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_pkey TO team_schedule_assignment_pkey';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_shift_id_fkey TO team_schedule_assignment_shift_id_fkey';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_team_agrees TO team_schedule_assignment_team_agrees';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_team_id_fkey TO team_schedule_assignment_team_id_fkey';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_team_key_fkey TO team_schedule_assignment_team_key_fkey';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_unique_slot TO team_schedule_assignment_unique_slot';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_user_id_fkey TO team_schedule_assignment_user_id_fkey';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_window_check TO team_schedule_assignment_window_check';
        EXECUTE 'ALTER TABLE team_schedule_assignment RENAME CONSTRAINT schedule_assignment_zone_id_fkey TO team_schedule_assignment_zone_id_fkey';
        EXECUTE 'ALTER TABLE team_schedule_assignment_activity RENAME CONSTRAINT schedule_assignment_activity_action_check TO team_schedule_assignment_activity_action_check';
        EXECUTE 'ALTER TABLE team_schedule_assignment_activity RENAME CONSTRAINT schedule_assignment_activity_pkey TO team_schedule_assignment_activity_pkey';
        EXECUTE 'ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_code_key TO team_schedule_shift_code_key';
        EXECUTE 'ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_end_minute_check TO team_schedule_shift_end_minute_check';
        EXECUTE 'ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_escalation_zone_check TO team_schedule_shift_escalation_zone_check';
        EXECUTE 'ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_pkey TO team_schedule_shift_pkey';
        EXECUTE 'ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_required_headcount_check TO team_schedule_shift_required_headcount_check';
        EXECUTE 'ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_start_minute_check TO team_schedule_shift_start_minute_check';
        EXECUTE 'ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_zone_family_check TO team_schedule_shift_zone_family_check';
        EXECUTE 'ALTER TABLE team_schedule_shift RENAME CONSTRAINT schedule_shift_zone_id_fkey TO team_schedule_shift_zone_id_fkey';
        EXECUTE 'ALTER TABLE team_schedule_zone RENAME CONSTRAINT schedule_zone_code_key TO team_schedule_zone_code_key';
        EXECUTE 'ALTER TABLE team_schedule_zone RENAME CONSTRAINT schedule_zone_pkey TO team_schedule_zone_pkey';
        EXECUTE 'ALTER TABLE team_schedule_zone RENAME CONSTRAINT schedule_zone_weekend_zone_id_fkey TO team_schedule_zone_weekend_zone_id_fkey';
    END IF;
END $$;

-- A plpgsql body is text, resolved when it runs, so renaming the type and the
-- table it names left the function still looking for schedule_tier_enum and
-- schedule_shift. Replaced rather than renamed for that reason. CREATE OR
-- REPLACE is idempotent on its own and needs no guard -- it only has to run
-- after the block above, whether that ran just now or on an earlier pass.
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

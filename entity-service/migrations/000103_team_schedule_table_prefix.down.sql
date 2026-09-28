-- Reverses 000103. Renames only; nothing it enforces changes.

ALTER TRIGGER team_schedule_assignment_matches_shift_trigger ON team_schedule_assignment
    RENAME TO schedule_assignment_matches_shift_trigger;
ALTER FUNCTION team_schedule_assignment_matches_shift()
    RENAME TO schedule_assignment_matches_shift;

ALTER TABLE team_schedule_absence RENAME TO schedule_absence;
ALTER TABLE team_schedule_absence_activity RENAME TO schedule_absence_activity;
ALTER TABLE team_schedule_absence_kind RENAME TO schedule_absence_kind;
ALTER TABLE team_schedule_assignment RENAME TO schedule_assignment;
ALTER TABLE team_schedule_assignment_activity RENAME TO schedule_assignment_activity;
ALTER TABLE team_schedule_shift RENAME TO schedule_shift;
ALTER TABLE team_schedule_zone RENAME TO schedule_zone;
ALTER TYPE team_schedule_absence_bucket_enum RENAME TO schedule_absence_bucket_enum;
ALTER TYPE team_schedule_day_scope_enum RENAME TO schedule_day_scope_enum;
ALTER TYPE team_schedule_shift_family_enum RENAME TO schedule_shift_family_enum;
ALTER TYPE team_schedule_source_enum RENAME TO schedule_source_enum;
ALTER TYPE team_schedule_tier_enum RENAME TO schedule_tier_enum;
ALTER INDEX idx_team_schedule_absence_activity_absence RENAME TO idx_schedule_absence_activity_absence;
ALTER INDEX idx_team_schedule_absence_activity_team RENAME TO idx_schedule_absence_activity_team;
ALTER INDEX idx_team_schedule_absence_activity_user RENAME TO idx_schedule_absence_activity_user;
ALTER INDEX idx_team_schedule_absence_kind_id RENAME TO idx_schedule_absence_kind_id;
ALTER INDEX idx_team_schedule_absence_span RENAME TO idx_schedule_absence_span;
ALTER INDEX idx_team_schedule_absence_team RENAME TO idx_schedule_absence_team;
ALTER INDEX idx_team_schedule_absence_user RENAME TO idx_schedule_absence_user;
ALTER INDEX idx_team_schedule_assignment_activity_assignment RENAME TO idx_schedule_assignment_activity_assignment;
ALTER INDEX idx_team_schedule_assignment_activity_team_date RENAME TO idx_schedule_assignment_activity_team_date;
ALTER INDEX idx_team_schedule_assignment_activity_user_date RENAME TO idx_schedule_assignment_activity_user_date;
ALTER INDEX idx_team_schedule_assignment_rota_date RENAME TO idx_schedule_assignment_rota_date;
ALTER INDEX idx_team_schedule_assignment_team_date RENAME TO idx_schedule_assignment_team_date;
ALTER INDEX idx_team_schedule_assignment_user_date RENAME TO idx_schedule_assignment_user_date;
ALTER INDEX idx_team_schedule_assignment_window RENAME TO idx_schedule_assignment_window;
ALTER INDEX idx_team_schedule_assignment_zone_tier_date RENAME TO idx_schedule_assignment_zone_tier_date;
ALTER INDEX idx_team_schedule_shift_family RENAME TO idx_schedule_shift_family;
ALTER INDEX idx_team_schedule_shift_zone_id RENAME TO idx_schedule_shift_zone_id;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT team_schedule_absence_kind_id_fkey TO schedule_absence_kind_id_fkey;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT team_schedule_absence_no_overlap TO schedule_absence_no_overlap;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT team_schedule_absence_pkey TO schedule_absence_pkey;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT team_schedule_absence_range_check TO schedule_absence_range_check;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT team_schedule_absence_team_key_fkey TO schedule_absence_team_key_fkey;
ALTER TABLE team_schedule_absence RENAME CONSTRAINT team_schedule_absence_user_id_fkey TO schedule_absence_user_id_fkey;
ALTER TABLE team_schedule_absence_activity RENAME CONSTRAINT team_schedule_absence_activity_action_check TO schedule_absence_activity_action_check;
ALTER TABLE team_schedule_absence_activity RENAME CONSTRAINT team_schedule_absence_activity_pkey TO schedule_absence_activity_pkey;
ALTER TABLE team_schedule_absence_kind RENAME CONSTRAINT team_schedule_absence_kind_code_key TO schedule_absence_kind_code_key;
ALTER TABLE team_schedule_absence_kind RENAME CONSTRAINT team_schedule_absence_kind_pkey TO schedule_absence_kind_pkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_no_overlap TO schedule_assignment_no_overlap;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_pkey TO schedule_assignment_pkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_shift_id_fkey TO schedule_assignment_shift_id_fkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_team_agrees TO schedule_assignment_team_agrees;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_team_id_fkey TO schedule_assignment_team_id_fkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_team_key_fkey TO schedule_assignment_team_key_fkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_unique_slot TO schedule_assignment_unique_slot;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_user_id_fkey TO schedule_assignment_user_id_fkey;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_window_check TO schedule_assignment_window_check;
ALTER TABLE team_schedule_assignment RENAME CONSTRAINT team_schedule_assignment_zone_id_fkey TO schedule_assignment_zone_id_fkey;
ALTER TABLE team_schedule_assignment_activity RENAME CONSTRAINT team_schedule_assignment_activity_action_check TO schedule_assignment_activity_action_check;
ALTER TABLE team_schedule_assignment_activity RENAME CONSTRAINT team_schedule_assignment_activity_pkey TO schedule_assignment_activity_pkey;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT team_schedule_shift_code_key TO schedule_shift_code_key;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT team_schedule_shift_end_minute_check TO schedule_shift_end_minute_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT team_schedule_shift_escalation_zone_check TO schedule_shift_escalation_zone_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT team_schedule_shift_pkey TO schedule_shift_pkey;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT team_schedule_shift_required_headcount_check TO schedule_shift_required_headcount_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT team_schedule_shift_start_minute_check TO schedule_shift_start_minute_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT team_schedule_shift_zone_family_check TO schedule_shift_zone_family_check;
ALTER TABLE team_schedule_shift RENAME CONSTRAINT team_schedule_shift_zone_id_fkey TO schedule_shift_zone_id_fkey;
ALTER TABLE team_schedule_zone RENAME CONSTRAINT team_schedule_zone_code_key TO schedule_zone_code_key;
ALTER TABLE team_schedule_zone RENAME CONSTRAINT team_schedule_zone_pkey TO schedule_zone_pkey;
ALTER TABLE team_schedule_zone RENAME CONSTRAINT team_schedule_zone_weekend_zone_id_fkey TO schedule_zone_weekend_zone_id_fkey;

-- Renaming the function does not rewrite its body: plpgsql resolves names when
-- it runs, so after the renames above it would still be looking for
-- team_schedule_tier_enum and team_schedule_shift, neither of which exists any
-- more. Every insert or update of shift_id, zone_id or tier would fail. So the
-- body is restored to what 000100 created, exactly as the up migration
-- replaced it going the other way.
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

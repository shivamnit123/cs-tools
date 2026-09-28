DROP INDEX IF EXISTS idx_team_schedule_assignment_activity_date;
DROP TRIGGER IF EXISTS team_schedule_zone_audit ON team_schedule_zone;
DROP TRIGGER IF EXISTS team_schedule_shift_audit ON team_schedule_shift;
DROP TRIGGER IF EXISTS team_schedule_absence_kind_audit ON team_schedule_absence_kind;
DROP TRIGGER IF EXISTS team_schedule_absence_audit ON team_schedule_absence;
DROP TRIGGER IF EXISTS team_schedule_assignment_audit ON team_schedule_assignment;
DROP FUNCTION IF EXISTS team_schedule_audit_row();
DROP TABLE IF EXISTS team_schedule_audit;

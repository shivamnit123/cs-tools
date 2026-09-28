ALTER TABLE schedule_assignment DROP CONSTRAINT IF EXISTS schedule_assignment_team_agrees;
ALTER TABLE schedule_absence DROP CONSTRAINT IF EXISTS schedule_absence_team_key_fkey;
ALTER TABLE schedule_assignment DROP CONSTRAINT IF EXISTS schedule_assignment_team_key_fkey;
ALTER TABLE team DROP CONSTRAINT IF EXISTS team_id_key_unique;
ALTER TABLE team DROP CONSTRAINT IF EXISTS team_key_unique;
ALTER TABLE team DROP COLUMN IF EXISTS key;

-- team_key had no catalogue anywhere in the database: a free VARCHAR(64) on
-- both rota fact tables, with no lookup, no foreign key and no check. A typo'd
-- or renamed key returned an empty rota rather than an error -- the same drift
-- risk that turned schedule_absence_kind from an enum into a table in 0154,
-- except here there was not even a row to add.
--
-- The catalogue already exists: it is the team table. What was missing is a
-- stable key to join on. Teams are referred to by a lowercase slug throughout
-- this feature (the CSM_TEAM_REGISTRY entries, the seed, the importer), while
-- team.name carries the display form, so the slug becomes a column of its own
-- rather than an expression nothing can key off.
ALTER TABLE team ADD COLUMN IF NOT EXISTS key VARCHAR(64);

UPDATE team SET key = lower(name) WHERE key IS NULL;

-- Two teams whose names differ only by case would collide here. None do; this
-- fails loudly at migration time rather than silently later if that changes.
ALTER TABLE team ALTER COLUMN key SET NOT NULL;

-- Postgres has no ADD CONSTRAINT IF NOT EXISTS; dropping each one first (a
-- no-op the first time this runs) is what makes re-running this file safe.
-- CASCADE, because on a re-run the fact-table foreign keys below already
-- depend on both unique constraints -- this file recreates every one of
-- them further down, so dropping the dependents here costs nothing.
ALTER TABLE team DROP CONSTRAINT IF EXISTS team_key_unique CASCADE;
ALTER TABLE team ADD CONSTRAINT team_key_unique UNIQUE (key);

-- Lets the composite foreign key below reference (id, key) as a pair.
ALTER TABLE team DROP CONSTRAINT IF EXISTS team_id_key_unique CASCADE;
ALTER TABLE team ADD CONSTRAINT team_id_key_unique UNIQUE (id, key);

-- ── Every team the rota refers to must be a row ────────────────────────
--
-- 0152 kept team_key and team_id side by side and said the registry has no
-- foreign key to offer, so a team could live in CSM_TEAM_REGISTRY with no team
-- row at all. That is what this supersedes: a key with no row was exactly the
-- silent empty rota review raised, and the catalogue endpoint builds the team
-- list from this table now, so a team without a row is invisible to the page
-- whether or not a constraint says so.
--
-- Checked first, and loudly. The ALTER below would refuse on its own, but with
-- a message naming a constraint rather than the keys that are wrong.
DO $$
DECLARE missing TEXT;
BEGIN
    SELECT string_agg(DISTINCT k, ', ') INTO missing FROM (
        SELECT team_key AS k FROM schedule_assignment
        UNION
        SELECT team_key FROM schedule_absence
    ) used
    WHERE NOT EXISTS (SELECT 1 FROM team t WHERE t.key = used.k);

    IF missing IS NOT NULL THEN
        RAISE EXCEPTION
            'the rota refers to teams with no team row: %. Add them to the team table (name and key) before applying this migration -- the schedule catalogue is built from that table now, so a team without a row cannot be shown or rostered.',
            missing;
    END IF;
END $$;

-- ── The fact tables now point at a real row ───────────────────────────────
ALTER TABLE schedule_assignment DROP CONSTRAINT IF EXISTS schedule_assignment_team_key_fkey;
ALTER TABLE schedule_assignment
    ADD CONSTRAINT schedule_assignment_team_key_fkey
    FOREIGN KEY (team_key) REFERENCES team (key) ON UPDATE CASCADE;

ALTER TABLE schedule_absence DROP CONSTRAINT IF EXISTS schedule_absence_team_key_fkey;
ALTER TABLE schedule_absence
    ADD CONSTRAINT schedule_absence_team_key_fkey
    FOREIGN KEY (team_key) REFERENCES team (key) ON UPDATE CASCADE;

-- ── and team_id can no longer disagree with team_key ──────────────────────
--
-- schedule_assignment carries both: team_id for a direct join, team_key for
-- the registry-only teams that have no row of their own. Nothing tied them
-- together, so a row could name one team by id and a different one by key.
--
-- MATCH SIMPLE is what is wanted rather than a gap: when team_id is null the
-- composite constraint stands down, which is exactly the registry-only case,
-- and team_key is still checked on its own by the foreign key above.
ALTER TABLE schedule_assignment DROP CONSTRAINT IF EXISTS schedule_assignment_team_agrees;
ALTER TABLE schedule_assignment
    ADD CONSTRAINT schedule_assignment_team_agrees
    FOREIGN KEY (team_id, team_key) REFERENCES team (id, key) ON UPDATE CASCADE;

-- schedule_absence deliberately carries no team_id. It is written and read by
-- key alone -- no query joins an absence to a team row -- and a second way to
-- say the same thing is a second thing to keep in step. The key is now
-- constrained, which is what the id was providing here.
COMMENT ON COLUMN schedule_assignment.team_key IS
    'The team this assignment belongs to, by catalogue key, and a real row '
    'in team. This supersedes the note in 0152 that the registry has no '
    'foreign key to offer: the schedule catalogue is served from the team '
    'table now, so a key with no row could not be displayed or rostered even '
    'without the constraint. team_id is kept alongside it, and the pair is '
    'constrained to agree.';

COMMENT ON COLUMN schedule_absence.team_key IS
    'The team this absence belongs to, by catalogue key. No team_id column on '
    'purpose: nothing joins an absence to a team row, and the foreign key on '
    'this column is what integrity the id would have added.';

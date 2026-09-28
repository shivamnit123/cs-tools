-- What changed about who is away, who changed it, and what it was before.
--
-- The same argument as schedule_assignment_activity (000094), for the other
-- half of the rota: once a lead can mark leave from the roster, overwriting or
-- trimming an absence destroys the only record that it was ever there. "Who
-- cancelled my leave?" is the same question as "who took me off Tuesday?" and
-- deserves the same answer.
--
-- A separate table rather than a widened schedule_assignment_activity, because
-- that table is NOT NULL on rota_date and shift_code -- an absence has neither.
-- It is a span, not a day, and it belongs to no window. Making those columns
-- nullable to fit it would mean every reader of the assignment history has to
-- ask which kind of row it is looking at.
CREATE TABLE IF NOT EXISTS schedule_absence_activity (
    id              UUID PRIMARY KEY,
    created_on      TIMESTAMPTZ NOT NULL,
    created_by      VARCHAR(255) NOT NULL,

    -- Not a foreign key, for the reason 000094 gives: a delete is the change
    -- most worth keeping, and ON DELETE CASCADE would erase exactly that.
    absence_id      UUID NOT NULL,

    -- Denormalised so history survives the row it describes.
    user_id         UUID NOT NULL,
    team_key        VARCHAR(100) NOT NULL,
    kind_code       VARCHAR(64) NOT NULL,
    starts_on       DATE NOT NULL,
    -- Null for an absence with no end date, the same as the row it describes.
    ends_on         DATE,

    action          VARCHAR(20) NOT NULL,
    field_name      VARCHAR(255),
    old_value       VARCHAR(255),
    new_value       VARCHAR(255),

    -- The person who made the change, not the person the row is about.
    actor_email     VARCHAR(255) NOT NULL,
    note            TEXT,

    -- TRIMMED is this table's own: clearing part of a span shortens the
    -- absence rather than removing it, and recording that as a delete would
    -- say the leave was cancelled when most of it still stands.
    CONSTRAINT schedule_absence_activity_action_check
        CHECK (action IN ('CREATED', 'UPDATED', 'DELETED', 'TRIMMED'))
);

CREATE INDEX IF NOT EXISTS idx_schedule_absence_activity_absence
    ON schedule_absence_activity (absence_id, created_on DESC);
CREATE INDEX IF NOT EXISTS idx_schedule_absence_activity_user
    ON schedule_absence_activity (user_id, starts_on);
CREATE INDEX IF NOT EXISTS idx_schedule_absence_activity_team
    ON schedule_absence_activity (team_key, starts_on);

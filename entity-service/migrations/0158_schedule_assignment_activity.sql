-- What changed on the rota, who changed it, and what it was before.
--
-- Once a lead can edit, overwriting an assignment destroys the only record of
-- who was on it. "Why am I on Saturday?" and "who took me off Tuesday?" become
-- unanswerable, and they are the first two questions anyone asks of a rota they
-- did not expect.
--
-- Deliberately the same shape as work_item_activity (000056) rather than a new
-- design: field_name / old_value / new_value / user_email is already how this
-- codebase records a change, and a second grammar for the same idea would mean
-- two things to learn and two places to fix.
--
-- Rows are written in the same transaction as the mutation they describe. An
-- activity row without its change, or a change without its row, is worse than
-- either alone.
CREATE TABLE IF NOT EXISTS schedule_assignment_activity (
    id              UUID PRIMARY KEY,
    created_on      TIMESTAMPTZ NOT NULL,
    created_by      VARCHAR(255) NOT NULL,

    -- Not a foreign key, on purpose. A delete is the change most worth keeping,
    -- and ON DELETE CASCADE would erase exactly that record. The id is kept so
    -- a surviving assignment can still be traced.
    assignment_id   UUID NOT NULL,

    -- Denormalised so history survives the row it describes. After a delete
    -- there is nothing left to join to.
    user_id         UUID NOT NULL,
    team_key        VARCHAR(100) NOT NULL,
    rota_date       DATE NOT NULL,
    shift_code      VARCHAR(100) NOT NULL,

    action          VARCHAR(20) NOT NULL,
    field_name      VARCHAR(255),
    old_value       VARCHAR(255),
    new_value       VARCHAR(255),

    -- The person who made the change, not the person the row is about.
    actor_email     VARCHAR(255) NOT NULL,
    note            TEXT,

    CONSTRAINT schedule_assignment_activity_action_check
        CHECK (action IN ('CREATED', 'UPDATED', 'DELETED'))
);

-- "What happened to this assignment" and "what did this engineer's rota do".
CREATE INDEX IF NOT EXISTS idx_schedule_assignment_activity_assignment
    ON schedule_assignment_activity (assignment_id, created_on DESC);
CREATE INDEX IF NOT EXISTS idx_schedule_assignment_activity_user_date
    ON schedule_assignment_activity (user_id, rota_date);
-- "what changed on my team this week", which is what a lead opens it for.
CREATE INDEX IF NOT EXISTS idx_schedule_assignment_activity_team_date
    ON schedule_assignment_activity (team_key, rota_date);

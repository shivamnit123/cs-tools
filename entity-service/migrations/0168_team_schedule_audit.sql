-- One audit trail for everything the rota owns, written by the database
-- rather than by the application.
--
-- The two activity tables (0158, 0159) record what the service does, and
-- they stay: they are typed, indexed by team and date, and they are what the
-- page reads to say who changed a cell. What they cannot do is record a write
-- that never went through the service. The seed and the roster importer write
-- straight to the tables, which is why 12,684 assignments had 41 history rows
-- between them -- and a hand-run UPDATE during an incident would leave none at
-- all. The catalogue has never been audited by anything.
--
-- So this is the layer underneath: a trigger on each table, capturing the row
-- before and after as JSONB. Nothing can bypass it, because it is not asking
-- the caller to remember anything.
CREATE TABLE IF NOT EXISTS team_schedule_audit (
    id              BIGSERIAL PRIMARY KEY,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    table_name      TEXT NOT NULL,
    -- Every audited table is keyed by a uuid id, so one column addresses them
    -- all. Null only if that ever stops being true.
    row_id          UUID,

    action          TEXT NOT NULL,

    -- Who, as far as the database can tell. Preferred order: an actor the
    -- application set for the transaction, then the row's own updated_by or
    -- created_by. On a DELETE the row can only offer its last writer, which is
    -- not necessarily the person deleting it -- app.actor is what closes that,
    -- where the caller sets it.
    actor           VARCHAR(255),

    old_row         JSONB,
    new_row         JSONB,
    -- Which columns actually moved, so a reader does not have to diff two
    -- JSONB blobs to find out. Null for INSERT, DELETE and BASELINE.
    changed_fields  TEXT[],

    CONSTRAINT team_schedule_audit_action_check
        CHECK (action IN ('INSERT', 'UPDATE', 'DELETE', 'BASELINE'))
);

-- "What happened to this row" -- the common read, newest first.
CREATE INDEX IF NOT EXISTS idx_team_schedule_audit_row
    ON team_schedule_audit (table_name, row_id, changed_at DESC);
-- "What happened at all, lately", for an operator rather than a page.
CREATE INDEX IF NOT EXISTS idx_team_schedule_audit_when
    ON team_schedule_audit (changed_at DESC);
-- "Everything this person did", which is the question an audit gets asked.
CREATE INDEX IF NOT EXISTS idx_team_schedule_audit_actor
    ON team_schedule_audit (actor, changed_at DESC);

CREATE OR REPLACE FUNCTION team_schedule_audit_row() RETURNS TRIGGER AS $$
DECLARE
    v_old JSONB;
    v_new JSONB;
    v_actor TEXT;
    v_fields TEXT[];
BEGIN
    IF TG_OP = 'DELETE' THEN
        v_old := to_jsonb(OLD);
    ELSIF TG_OP = 'INSERT' THEN
        v_new := to_jsonb(NEW);
    ELSE
        v_old := to_jsonb(OLD);
        v_new := to_jsonb(NEW);

        -- A row touched without anything of substance moving is not a change
        -- worth a line in the history. updated_on and updated_by move on every
        -- write by construction, so they do not count as movement themselves.
        SELECT array_agg(e.key ORDER BY e.key) INTO v_fields
          FROM jsonb_each(v_new) e
         WHERE e.key NOT IN ('updated_on', 'updated_by')
           AND v_new -> e.key IS DISTINCT FROM v_old -> e.key;

        IF v_fields IS NULL THEN
            RETURN NULL;
        END IF;
    END IF;

    v_actor := COALESCE(
        NULLIF(current_setting('app.actor', true), ''),
        v_new ->> 'updated_by', v_new ->> 'created_by',
        v_old ->> 'updated_by', v_old ->> 'created_by');

    INSERT INTO team_schedule_audit
        (table_name, row_id, action, actor, old_row, new_row, changed_fields)
    VALUES (
        TG_TABLE_NAME,
        COALESCE((v_new ->> 'id')::uuid, (v_old ->> 'id')::uuid),
        TG_OP, v_actor, v_old, v_new, v_fields);

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- The five tables that hold state. The two activity tables are append-only
-- history already; auditing them would record the same change twice and grow
-- without bound.
DROP TRIGGER IF EXISTS team_schedule_assignment_audit ON team_schedule_assignment;
CREATE TRIGGER team_schedule_assignment_audit
    AFTER INSERT OR UPDATE OR DELETE ON team_schedule_assignment
    FOR EACH ROW EXECUTE FUNCTION team_schedule_audit_row();
DROP TRIGGER IF EXISTS team_schedule_absence_audit ON team_schedule_absence;
CREATE TRIGGER team_schedule_absence_audit
    AFTER INSERT OR UPDATE OR DELETE ON team_schedule_absence
    FOR EACH ROW EXECUTE FUNCTION team_schedule_audit_row();
DROP TRIGGER IF EXISTS team_schedule_absence_kind_audit ON team_schedule_absence_kind;
CREATE TRIGGER team_schedule_absence_kind_audit
    AFTER INSERT OR UPDATE OR DELETE ON team_schedule_absence_kind
    FOR EACH ROW EXECUTE FUNCTION team_schedule_audit_row();
DROP TRIGGER IF EXISTS team_schedule_shift_audit ON team_schedule_shift;
CREATE TRIGGER team_schedule_shift_audit
    AFTER INSERT OR UPDATE OR DELETE ON team_schedule_shift
    FOR EACH ROW EXECUTE FUNCTION team_schedule_audit_row();
DROP TRIGGER IF EXISTS team_schedule_zone_audit ON team_schedule_zone;
CREATE TRIGGER team_schedule_zone_audit
    AFTER INSERT OR UPDATE OR DELETE ON team_schedule_zone
    FOR EACH ROW EXECUTE FUNCTION team_schedule_audit_row();

-- A baseline for what is already here. The history of these rows cannot be
-- recovered -- it was never written -- but leaving them with no audit row at
-- all makes "this row has no history" ambiguous between "never touched" and
-- "predates the audit". BASELINE says which.
INSERT INTO team_schedule_audit (changed_at, table_name, row_id, action, actor, new_row)
SELECT now(), 'team_schedule_assignment', a.id, 'BASELINE', a.created_by, to_jsonb(a) FROM team_schedule_assignment a;
INSERT INTO team_schedule_audit (changed_at, table_name, row_id, action, actor, new_row)
SELECT now(), 'team_schedule_absence', a.id, 'BASELINE', a.created_by, to_jsonb(a) FROM team_schedule_absence a;
INSERT INTO team_schedule_audit (changed_at, table_name, row_id, action, actor, new_row)
SELECT now(), 'team_schedule_absence_kind', a.id, 'BASELINE', a.created_by, to_jsonb(a) FROM team_schedule_absence_kind a;
INSERT INTO team_schedule_audit (changed_at, table_name, row_id, action, actor, new_row)
SELECT now(), 'team_schedule_shift', a.id, 'BASELINE', a.created_by, to_jsonb(a) FROM team_schedule_shift a;
INSERT INTO team_schedule_audit (changed_at, table_name, row_id, action, actor, new_row)
SELECT now(), 'team_schedule_zone', a.id, 'BASELINE', a.created_by, to_jsonb(a) FROM team_schedule_zone a;

-- The roster marks which cells a person has changed, which is a lookup by
-- date window across every team on screen. The existing indexes on this table
-- both lead with something else (assignment_id, user_id, team_key), so that
-- window would be a scan.
CREATE INDEX IF NOT EXISTS idx_team_schedule_assignment_activity_date
    ON team_schedule_assignment_activity (rota_date, created_on DESC);

ALTER TABLE schedule_absence_kind
    ALTER COLUMN bucket TYPE VARCHAR(20) USING bucket::text;

ALTER TABLE schedule_absence_kind
    ADD CONSTRAINT schedule_absence_kind_bucket_check
    CHECK (bucket IN ('LEAVE', 'ALLOCATION', 'EXCLUDED'));

DROP TYPE IF EXISTS schedule_absence_bucket_enum;

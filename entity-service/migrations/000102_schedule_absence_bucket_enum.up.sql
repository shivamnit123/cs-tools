-- schedule_absence_kind.bucket was a VARCHAR with a CHECK IN (...), while
-- family, tier, day_scope and source were all enums. 000090's own rule for
-- which is which: a value is an enum when it is structural -- when it changes
-- how the service or the UI behaves rather than only what they display.
--
-- bucket is exactly that. It decides whether a day is leave that still needs
-- covering, an allocation, or an exclusion from the rota altogether, and the
-- views branch on it. Applying the rule everywhere except here was the
-- inconsistency raised in ERD review.
--
-- The kinds themselves stay a table, which is the other half of 000090's rule
-- and unaffected: adding a reason is still an insert. It is the three
-- categories those reasons fall into that are structural, and those change
-- with the code that branches on them.
CREATE TYPE schedule_absence_bucket_enum AS ENUM ('LEAVE', 'ALLOCATION', 'EXCLUDED');

ALTER TABLE schedule_absence_kind DROP CONSTRAINT IF EXISTS schedule_absence_kind_bucket_check;

ALTER TABLE schedule_absence_kind
    ALTER COLUMN bucket TYPE schedule_absence_bucket_enum
    USING bucket::schedule_absence_bucket_enum;

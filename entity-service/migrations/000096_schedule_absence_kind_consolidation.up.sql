-- Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
--
-- WSO2 LLC. licenses this file to you under the Apache License,
-- Version 2.0 (the "License"); you may not use this file except
-- in compliance with the License.
-- You may obtain a copy of the License at
--
-- http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing,
-- software distributed under the License is distributed on an
-- "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
-- KIND, either express or implied.  See the License for the
-- specific language governing permissions and limitations
-- under the License.

-- Allocations: a few kinds, and who the time is for.
--
-- The rota sheet this replaces grew a tag per customer -- Allo-<customer>,
-- one for each engagement anyone was ever lent to -- so every new engagement
-- meant a new tag, and the question a lead asks ("where is this engineer, and
-- for whom?") had its answer split across two things at once. Here the kind
-- says what sort of time it is and `allocated_to` says for whom:
--
--   Customer allocation, on site     CUSTOMER_ONSITE   allocated_to = customer
--   Customer allocation, off site    CUSTOMER_OFFSITE  allocated_to = customer
--   RnD (product team, internal)     RND               allocated_to = product team
--   Brazil rotation (special)        ALLO_BR           -- its own kind, see below
--
-- A new customer is a value, not a tag. The retired kinds are folded in:
--   CUSTOMER (unspecified), ALLO_EXT  -> CUSTOMER_OFFSITE
--   ALLO_CRIS                         -> CUSTOMER_OFFSITE, allocated_to 'CRIS'
--   ALLO_INT                          -> RND
-- Unspecified customer time lands off site because that is the common case;
-- a lead corrects the few that were not.
--
-- The Brazil rotation keeps a kind of its own. It is neither customer nor
-- product work -- an engineer on it covers Brazil's hours for weeks at a time
-- -- and a lead planning the LK rota needs to see it as exactly that, not
-- lose it inside RnD.
--
-- Retired kinds are deactivated rather than deleted. kind_id is ON DELETE
-- RESTRICT, and the catalogue already lists active kinds only, so they
-- disappear from every picker and legend without orphaning anything.

ALTER TABLE schedule_absence ADD COLUMN IF NOT EXISTS allocated_to VARCHAR(100);
COMMENT ON COLUMN schedule_absence.allocated_to IS
  'Who an allocation is for: the customer for a customer allocation, the product team for RnD. NULL when not known or not an allocation.';

UPDATE schedule_absence a
   SET kind_id = target.id,
       allocated_to = COALESCE(a.allocated_to, CASE WHEN retired.code = 'ALLO_CRIS' THEN 'CRIS' END),
       updated_on = NOW(), updated_by = 'migration'
  FROM schedule_absence_kind retired, schedule_absence_kind target
 WHERE a.kind_id = retired.id
   AND (   (retired.code IN ('CUSTOMER', 'ALLO_EXT', 'ALLO_CRIS') AND target.code = 'CUSTOMER_OFFSITE')
        OR (retired.code = 'ALLO_INT' AND target.code = 'RND'));

UPDATE schedule_absence_kind
   SET is_active = FALSE, updated_on = NOW(), updated_by = 'migration'
 WHERE code IN ('CUSTOMER', 'ALLO_EXT', 'ALLO_CRIS', 'ALLO_INT');

UPDATE schedule_absence_kind
   SET label = 'RnD (product team)', short_code = 'RnD', updated_on = NOW(), updated_by = 'migration'
 WHERE code = 'RND';

UPDATE schedule_absence_kind
   SET label = 'Brazil rotation', short_code = 'BR', updated_on = NOW(), updated_by = 'migration'
 WHERE code = 'ALLO_BR';

-- Leave the sheet records that the catalogue had no kind for.
INSERT INTO schedule_absence_kind (code, short_code, label, bucket, colour_token, sort_order, created_by, updated_by)
VALUES
    ('SICK_LEAVE',    'SL', 'Sick leave',    'LEAVE', 'AL', 25, 'migration', 'migration'),
    ('MEDICAL_LEAVE', 'ML', 'Medical leave', 'LEAVE', 'AL', 26, 'migration', 'migration')
ON CONFLICT (code) DO NOTHING;

-- On-call turns the sheet rosters that had no window: the evening, the
-- weekend day and the weekday night each have an on-call variant, the same
-- way the morning and the weekend night already did.
INSERT INTO schedule_shift
    (code, label, family, zone_id, tier, day_scope, start_minute, end_minute,
     is_on_call, is_escalation, short_code, colour_token, sort_order, created_by, updated_by)
VALUES
    ('CRE_EVENING_OC',   'Evening 6-9pm on-call', 'CRE', NULL, NULL, 'WEEKDAY', 1080, 1260, TRUE, FALSE, '6-9p-OC', 'OC', 45, 'migration', 'migration'),
    ('CRE_WEEKEND_OC',   'Weekend on-call',       'CRE', NULL, NULL, 'WEEKEND',  360, 1260, TRUE, FALSE, 'WE-OC',   'OC', 65, 'migration', 'migration'),
    ('CRE_AMERICAS_OC',  'Non-LK on-call',        'CRE', NULL, NULL, 'WEEKDAY', 1260, 1800, TRUE, FALSE, 'NLK-OC',  'OC', 55, 'migration', 'migration')
ON CONFLICT (code) DO NOTHING;

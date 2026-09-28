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

-- Takes back what 000096 invented, and settles two names.
--
-- 000096 added three on-call windows from codes that turn up a handful of
-- times in the rota sheet -- "6-9pm -OC", "WE -OC", "NLK-OC". They are not
-- shifts anyone is rostered to: the evening, the weekend rotation and the
-- Americas cover each have one person, and there is no second on-call role
-- behind them. So they go, and the few rows on them move to the window they
-- really are, which has the same hours:
--   CRE_EVENING_OC  -> CRE_EVENING
--   CRE_WEEKEND_OC  -> CRE_WEEKEND
--   CRE_AMERICAS_OC -> CRE_AMERICAS
-- A row whose person already holds the real window that day is a duplicate,
-- and is dropped rather than moved (the slot is unique).
--
-- Medical leave goes the same way: the rota tracks sick leave, and one kind
-- answers the question a lead asks -- is this person off sick. Its rows become
-- sick leave.
--
-- And the weekend night is "Americas weekend", with "Americas weekend on-call"
-- behind it.

DELETE FROM schedule_assignment a
 USING schedule_shift s, schedule_shift base
 WHERE a.shift_id = s.id
   AND base.code = replace(s.code, '_OC', '')
   AND s.code IN ('CRE_EVENING_OC', 'CRE_WEEKEND_OC', 'CRE_AMERICAS_OC')
   AND EXISTS (SELECT 1 FROM schedule_assignment b
                WHERE b.user_id = a.user_id AND b.rota_date = a.rota_date AND b.shift_id = base.id);

UPDATE schedule_assignment a
   SET shift_id = base.id, is_on_call = base.is_on_call, updated_on = NOW(), updated_by = 'migration'
  FROM schedule_shift s, schedule_shift base
 WHERE a.shift_id = s.id
   AND base.code = replace(s.code, '_OC', '')
   AND s.code IN ('CRE_EVENING_OC', 'CRE_WEEKEND_OC', 'CRE_AMERICAS_OC');

DELETE FROM schedule_shift WHERE code IN ('CRE_EVENING_OC', 'CRE_WEEKEND_OC', 'CRE_AMERICAS_OC');

UPDATE schedule_absence a
   SET kind_id = sick.id, updated_on = NOW(), updated_by = 'migration'
  FROM schedule_absence_kind medical, schedule_absence_kind sick
 WHERE a.kind_id = medical.id AND medical.code = 'MEDICAL_LEAVE' AND sick.code = 'SICK_LEAVE';

DELETE FROM schedule_absence_kind WHERE code = 'MEDICAL_LEAVE';

UPDATE schedule_shift SET label = 'Americas weekend',         updated_on = NOW(), updated_by = 'migration' WHERE code = 'CRE_WEEKEND_NIGHT';
UPDATE schedule_shift SET label = 'Americas weekend on-call', updated_on = NOW(), updated_by = 'migration' WHERE code = 'CRE_WEEKEND_NIGHT_OC';

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

-- Reverses the vocabulary, not the data.
--
-- Rows the up path moved onto CUSTOMER_OFFSITE and RND stay there: it did not
-- record which kind each came from, and a row created since may belong on
-- either side. The added shifts and leave kinds are removed only when nothing
-- has been rostered or recorded against them -- both are ON DELETE RESTRICT,
-- and silently deleting a year of rota to make a down path succeed would be
-- the wrong trade.

DELETE FROM schedule_shift s
 WHERE s.code IN ('CRE_EVENING_OC', 'CRE_WEEKEND_OC', 'CRE_AMERICAS_OC')
   AND NOT EXISTS (SELECT 1 FROM schedule_assignment a WHERE a.shift_id = s.id);

DELETE FROM schedule_absence_kind k
 WHERE k.code IN ('SICK_LEAVE', 'MEDICAL_LEAVE')
   AND NOT EXISTS (SELECT 1 FROM schedule_absence a WHERE a.kind_id = k.id);

UPDATE schedule_absence_kind
   SET is_active = TRUE, updated_on = NOW(), updated_by = 'migration'
 WHERE code IN ('CUSTOMER', 'ALLO_EXT', 'ALLO_CRIS', 'ALLO_INT');

UPDATE schedule_absence_kind SET label = 'R&D', short_code = 'R&D', updated_on = NOW() WHERE code = 'RND';
UPDATE schedule_absence_kind SET label = 'Allocated — BR', short_code = 'BR', updated_on = NOW() WHERE code = 'ALLO_BR';

ALTER TABLE schedule_absence DROP COLUMN IF EXISTS allocated_to;

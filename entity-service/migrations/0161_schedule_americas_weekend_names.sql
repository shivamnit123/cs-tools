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

-- The weekend night is two roles, and the catalogue named neither of them.
--
--   CRE_WEEKEND_NIGHT     "Weekend non-LK cover" -> Americas weekend rota
--                         the person covering the Americas' weekend night
--   CRE_WEEKEND_NIGHT_OC  "Weekend on-call"      -> Americas weekend on-call
--                         the second person, backing them up
--
-- "Weekend on-call" said nothing about whose weekend, and it was also the name
-- 0160 gave the weekend-day on-call -- two windows, one name, printed side
-- by side on the day view. That one becomes "Weekend rotation on-call", which
-- is what it is: on call for the weekend rotation.
--
-- Labels only. The codes are what the rota, the import and the seed key on,
-- and none of them change.
UPDATE schedule_shift SET label = 'Americas weekend rota',    updated_on = NOW(), updated_by = 'migration' WHERE code = 'CRE_WEEKEND_NIGHT';
UPDATE schedule_shift SET label = 'Americas weekend on-call', updated_on = NOW(), updated_by = 'migration' WHERE code = 'CRE_WEEKEND_NIGHT_OC';
UPDATE schedule_shift SET label = 'Weekend rotation on-call', updated_on = NOW(), updated_by = 'migration' WHERE code = 'CRE_WEEKEND_OC';

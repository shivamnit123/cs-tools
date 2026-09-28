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

-- Restores the vocabulary, not the data: the windows and the kind come back,
-- but the rows that were moved onto the real windows, and the medical leave
-- that became sick leave, stay where they are -- which kind each came from
-- was not recorded.

INSERT INTO schedule_shift
    (code, label, family, zone_id, tier, day_scope, start_minute, end_minute,
     is_on_call, is_escalation, short_code, colour_token, sort_order, created_by, updated_by)
VALUES
    ('CRE_EVENING_OC',  'Evening 6-9pm on-call',    'CRE', NULL, NULL, 'WEEKDAY', 1080, 1260, TRUE, FALSE, '6-9p-OC', 'OC', 45, 'migration', 'migration'),
    ('CRE_WEEKEND_OC',  'Weekend rotation on-call', 'CRE', NULL, NULL, 'WEEKEND',  360, 1260, TRUE, FALSE, 'WE-OC',   'OC', 65, 'migration', 'migration'),
    ('CRE_AMERICAS_OC', 'Non-LK on-call',           'CRE', NULL, NULL, 'WEEKDAY', 1260, 1800, TRUE, FALSE, 'NLK-OC',  'OC', 55, 'migration', 'migration')
ON CONFLICT (code) DO NOTHING;

INSERT INTO schedule_absence_kind (code, short_code, label, bucket, colour_token, sort_order, created_by, updated_by)
VALUES ('MEDICAL_LEAVE', 'ML', 'Medical leave', 'LEAVE', 'AL', 26, 'migration', 'migration')
ON CONFLICT (code) DO NOTHING;

UPDATE schedule_shift SET label = 'Americas weekend rota', updated_on = NOW(), updated_by = 'migration' WHERE code = 'CRE_WEEKEND_NIGHT';

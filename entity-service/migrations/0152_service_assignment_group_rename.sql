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

-- service.assignment_group_id (added by 0075_add_group_references) was
-- always NULL in practice -- it was fed from the backing data source's own
-- "assignment_group" field, which is unpopulated on every real record
-- checked. service.support_group_id (also added by 0075) is the field
-- that's actually live and used for CI-level routing/incident assignment.
-- Mirrors the equivalent rename already applied to the CMDB sync job's own
-- copy of this table (a separate migration chain, tracked separately) --
-- dropping the dead column and renaming the real one to the name CS
-- engineers actually mean by "the service's assignment group", matching the
-- assignment_group_id vocabulary used elsewhere (e.g. work_item).
DROP INDEX IF EXISTS idx_service_assignment_group_id;
ALTER TABLE service DROP COLUMN IF EXISTS assignment_group_id;

ALTER TABLE service RENAME COLUMN support_group_id TO assignment_group_id;
ALTER INDEX idx_service_support_group_id RENAME TO idx_service_assignment_group_id;
ALTER TABLE service RENAME CONSTRAINT service_support_group_id_fkey TO service_assignment_group_id_fkey;

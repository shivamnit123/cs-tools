// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

import { devBypassAccessCheck, devViewOverride } from "@config/devFlags";
import { useCurrentUser } from "@context/current-user/CurrentUserContext";

/**
 * The two independent views this app renders behind one shared top bar:
 * "cs-abt" is CSM Portal's own nav (Dashboard, Support, Operations, ...) for
 * CS/ABT engineers; "sales-sa" is the ported Support Portal Lite nav (Cases,
 * Accounts, Projects, Team schedule, User scan, Customer health, Usage
 * metrics) for Sales/Solutions-Architecture staff. Unlike the earlier merge
 * (where SPL was just one more item inside the CS nav), the two are now
 * mutually exclusive: a user sees one full nav or the other, never both.
 */
export type PortalView = "cs-abt" | "sales-sa";

/**
 * Which view the signed-in user should see. Drives CsmSideBar's entire left
 * nav and RootLanding's default destination — see both for where this is
 * consumed.
 *
 * Real detection reads the "sales_solutions" portal role off `GET /users/me`
 * (the same server-authoritative `roles` array `usePortalAccess` reads the
 * other 8 portal roles from — see `internal/handler/access.go`'s
 * `AccessConfig.SalesSolutions`), not a client-side Asgardeo-groups decode —
 * so this can never disagree with what the backend itself thinks the caller
 * is. `AUTH_SALES_SOLUTIONS_ROLES` unset is a normal, supported state (every
 * caller resolves to "cs-abt"), not a misconfiguration.
 *
 * NOT YET migrated: the actual `/spl/*` route/API enforcement
 * (`SplRouteGuard`/`useSplAccess`, and the backend's own `internal/splauth`)
 * still checks raw Asgardeo groups, unrelated to this role — a real,
 * currently-open follow-up (see the Asgardeo Role Catalogue memory note).
 * Until that migrates too, this hook only decides which NAV renders; it is
 * not itself a security boundary for SPL's data.
 *
 * `devViewOverride` (authConfig.ts) lets local testing force either view
 * regardless of the signed-in account's real roles — set
 * `CSM_PORTAL_DEV_VIEW_OVERRIDE` in a local config.js. Under
 * `devBypassAccessCheck` alone (no explicit override), defaults to "cs-abt".
 */
export function usePortalView(): PortalView {
  let roles: string[] | undefined;
  try {
    // useCurrentUser always runs its useContext before it can throw, so the
    // hook order is identical on every render — same pattern as
    // usePortalAccess, which this mirrors; the lint rule can't see that.
    // eslint-disable-next-line react-hooks/rules-of-hooks
    roles = useCurrentUser().user?.roles;
  } catch {
    roles = undefined;
  }
  if (devViewOverride) return devViewOverride;
  if (devBypassAccessCheck) return "cs-abt";
  return roles?.includes("sales_solutions") ? "sales-sa" : "cs-abt";
}

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

// Whether the signed-in user should see the SPL (Support Portal Lite)
// section at all — the "is this Sales/SA staff" audience gate, distinct
// from useSplPermissions.ts's fine-grained action gates.
//
// Reads the "sales_solutions" portal role off `GET /users/me` (the same
// server-authoritative `roles` array usePortalView reads it from — see
// `internal/handler/access.go`'s `AccessConfig.SalesSolutions`), not a
// client-side Asgardeo-groups decode: this used to be the one deliberate
// exception to this app's backend-`roles` convention, but per the actual
// Asgardeo role catalogue (roles are already returned by `/users/me`,
// there's nothing left for the frontend to re-derive from IdP claims),
// that exception is no longer warranted and has been removed.
//
// Real enforcement is server-side: every /spl/* route on the Go backend
// re-checks PermSPLAccess (internal/handler/access.go), granted only by
// the sales_solutions role. A caller who reaches an SPL screen without
// the role sees a 403 from every call it makes, same as any other
// tampered/stale-claim scenario in this app.

import { useMemo } from "react";
import { useCurrentUser } from "@context/current-user/CurrentUserContext";
import { devBypassAccessCheck } from "@config/devFlags";

export interface SplAccess {
  /** False until the signed-in user's profile has been resolved — hold gated UI until it clears. */
  ready: boolean;
  hasAccess: boolean;
}

// Must stay byte-for-byte in sync with the backend's AccessGuard portalRoles
// key (apps/csm-portal/backend/internal/handler/access.go) and the identical
// literal usePortalView.ts checks to pick the SPL nav.
const SALES_SOLUTIONS_ROLE = "sales_solutions";

export function useSplAccess(): SplAccess {
  let roles: string[] | undefined;
  let isLoading = false;
  try {
    // useCurrentUser always runs its useContext before it can throw, so the
    // hook order is identical on every render — same pattern as
    // usePortalAccess/usePortalView, which this mirrors.
    const ctx = useCurrentUser();
    roles = ctx.user?.roles;
    isLoading = ctx.isLoading;
  } catch {
    roles = undefined;
  }

  return useMemo<SplAccess>(() => {
    // TEMPORARY / LOCAL DEV ONLY — see authConfig.ts's devBypassAccessCheck.
    // Short-circuits SPL's own audience gate so the section shows up even
    // when the signed-in account has no portal roles provisioned yet.
    if (devBypassAccessCheck) return { ready: true, hasAccess: true };
    if (isLoading) return { ready: false, hasAccess: false };
    return { ready: true, hasAccess: (roles ?? []).includes(SALES_SOLUTIONS_ROLE) };
  }, [roles, isLoading]);
}

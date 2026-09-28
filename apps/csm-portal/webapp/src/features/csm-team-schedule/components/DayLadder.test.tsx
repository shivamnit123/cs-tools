/**
 * Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

import { render, screen } from "@testing-library/react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import DayLadder, { type LadderLane } from "./DayLadder";
import {
  ANNUAL_LEAVE,
  EVENING,
  REGULAR,
  REGULAR_IND,
  TZ,
  ZONES,
  absence,
  assignment,
  scopeControls,
  shift,
  shiftMap,
} from "../test/fixtures";

// A card on the ladder measures itself, to say when it is holding more than
// it can show. jsdom has no ResizeObserver and never lays anything out, so a
// stub that never fires is both enough and honest: these tests are about what
// the ladder puts in a card, not how tall the card turns out.
beforeAll(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
    },
  );
});
afterAll(() => vi.unstubAllGlobals());

/** Wednesday 23 September 2026. */
const WEDNESDAY = new Date(2026, 8, 23);
const ISO = "2026-09-23";

/** The morning window and the on-call variant of it, same hours. */
const MORNING = shift({
  code: "CRE_MORNING",
  shortCode: "6-9a",
  label: "Morning 6-9am",
  startMinute: 360,
  endMinute: 540,
  sortOrder: 10,
});
const MORNING_OC = shift({
  code: "CRE_MORNING_OC",
  shortCode: "6-9a",
  label: "Morning 6-9am on-call",
  startMinute: 360,
  endMinute: 540,
  isOnCall: true,
  sortOrder: 11,
});

const SHIFTS = shiftMap(REGULAR, REGULAR_IND, EVENING, MORNING, MORNING_OC);

function lane(assignments: LadderLane["assignments"]): LadderLane {
  return { name: "Rotations", assignments, layout: "flat" };
}

function renderLadder(assignments: LadderLane["assignments"], absences = [] as ReturnType<typeof absence>[]) {
  return render(
    <DayLadder
      day={WEDNESDAY}
      tz={TZ}
      zoneLabel="IST"
      lanes={[lane(assignments)]}
      shifts={SHIFTS}
      zones={ZONES}
      absences={absences}
      absenceKinds={[ANNUAL_LEAVE]}
      {...scopeControls()}
    />,
  );
}

/** Nine to five on the day under test, in the fixture clock. */
function nineToFive(name: string, shiftCode: string, teamKey = "alpha") {
  return assignment({
    name,
    teamKey,
    rotaDate: ISO,
    shiftCode,
    startsAt: `${ISO}T03:30:00.000Z`,
    endsAt: `${ISO}T12:30:00.000Z`,
  });
}

describe("DayLadder: cards that share their hours", () => {
  it("holds a window and its on-call variant in one card, each naming its own people", () => {
    // Placed by time alone, two windows with the same hours drew on top of
    // each other and printed through one another.
    renderLadder([
      assignment({
        name: "Asela", rotaDate: ISO, shiftCode: MORNING.code,
        startsAt: `${ISO}T00:30:00.000Z`, endsAt: `${ISO}T03:30:00.000Z`,
      }),
      assignment({
        name: "Nuwan", rotaDate: ISO, shiftCode: MORNING_OC.code,
        startsAt: `${ISO}T00:30:00.000Z`, endsAt: `${ISO}T03:30:00.000Z`,
      }),
    ]);
    expect(screen.getByText(/Morning 6-9am · Morning 6-9am on-call/)).toBeInTheDocument();
    expect(screen.getByText("Asela")).toBeInTheDocument();
    expect(screen.getByText("Nuwan")).toBeInTheDocument();
  });

  it("folds a window that differs only by team into the crowded card", () => {
    // Regular hours and the India region shift are the same nine-to-five. A
    // card this size lists teams, which is the whole distinction, so a second
    // card said nothing the first one's second-team row would not.
    const people = Array.from({ length: 14 }, (_, i) => nineToFive(`Reg${i}`, REGULAR.code));
    renderLadder([...people, nineToFive("Akhil", REGULAR_IND.code, "bravo")]);

    expect(screen.getByText("Regular hours")).toBeInTheDocument();
    expect(screen.queryByText("India region shift")).not.toBeInTheDocument();
    expect(screen.getByText(/bravo/)).toBeInTheDocument();
  });

  it("never folds an on-call window into a crowded card", () => {
    // Being on call is not a fact about which team you are on, and a team
    // list cannot carry it.
    const people = Array.from({ length: 14 }, (_, i) =>
      assignment({
        name: `Morn${i}`, rotaDate: ISO, shiftCode: MORNING.code,
        startsAt: `${ISO}T00:30:00.000Z`, endsAt: `${ISO}T03:30:00.000Z`,
      }),
    );
    renderLadder([
      ...people,
      assignment({
        name: "OnCall", rotaDate: ISO, shiftCode: MORNING_OC.code,
        startsAt: `${ISO}T00:30:00.000Z`, endsAt: `${ISO}T03:30:00.000Z`,
      }),
    ]);
    expect(screen.getByText("Morning 6-9am on-call")).toBeInTheDocument();
  });
});

describe("DayLadder: who is not on the rota", () => {
  it("lists leave ahead of the allocations", () => {
    renderLadder(
      [nineToFive("Asela", REGULAR.code)],
      [absence({ name: "Nuwan", startsOn: ISO, endsOn: ISO })],
    );
    expect(screen.getByText("Annual leave")).toBeInTheDocument();
    expect(screen.getByText("Nuwan")).toBeInTheDocument();
  });
});

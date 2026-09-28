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
import { describe, expect, it } from "vitest";
import "@testing-library/jest-dom/vitest";
import WeekTable from "./WeekTable";
import {
  ANNUAL_LEAVE,
  EVENING,
  MONDAY,
  REGULAR,
  REGULAR_IND,
  absence,
  assignment,
  scopeControls,
  shiftMap,
} from "../test/fixtures";

const SHIFTS = shiftMap(REGULAR, REGULAR_IND, EVENING);

function renderWeek(over: Partial<React.ComponentProps<typeof WeekTable>> = {}) {
  return render(
    <WeekTable
      weekStart={MONDAY}
      assignments={[]}
      shifts={SHIFTS}
      absences={[]}
      absenceKinds={[ANNUAL_LEAVE]}
      {...scopeControls()}
      {...over}
    />,
  );
}

describe("WeekTable", () => {
  it("holds regular hours and the India region shift in one row", () => {
    // Both are 09:00-18:00 in the same zone on the same days: the code says
    // which team works it, not a different working day. The row lists teams,
    // so the second row said nothing this one's second-team entry does not.
    renderWeek({
      assignments: [
        assignment({ name: "Asela", rotaDate: "2026-09-21", shiftCode: REGULAR.code, teamKey: "alpha" }),
        assignment({ name: "Akhil", rotaDate: "2026-09-21", shiftCode: REGULAR_IND.code, teamKey: "bravo" }),
      ],
    });
    expect(screen.getByText("Regular hours")).toBeInTheDocument();
    expect(screen.queryByText("India region shift")).not.toBeInTheDocument();
    // Both engineers sit under that one row. The India shift's person is
    // there as one more name on regular hours, not as a row of their own.
    expect(screen.getByText("Asela")).toBeInTheDocument();
    expect(screen.getByText("Akhil")).toBeInTheDocument();
  });

  it("keeps a rotation in a row of its own", () => {
    renderWeek({
      assignments: [
        assignment({ name: "Asela", rotaDate: "2026-09-21", shiftCode: REGULAR.code }),
        assignment({ name: "Nuwan", rotaDate: "2026-09-21", shiftCode: EVENING.code }),
      ],
    });
    expect(screen.getByText("Regular hours")).toBeInTheDocument();
    expect(screen.getByText("Evening 6-9pm")).toBeInTheDocument();
  });

  it("counts people, not rows: one engineer on two rotations is one engineer", () => {
    const { container } = renderWeek({
      assignments: [
        assignment({ name: "Asela", userId: "u1", rotaDate: "2026-09-21", shiftCode: REGULAR.code }),
        assignment({ name: "Asela", userId: "u1", rotaDate: "2026-09-22", shiftCode: EVENING.code }),
      ],
    });
    expect(container.querySelector(".card-head .count")).toHaveTextContent("1");
  });

  it("shows who is away over the week", () => {
    renderWeek({
      assignments: [assignment({ name: "Asela", rotaDate: "2026-09-21", shiftCode: REGULAR.code })],
      absences: [absence({ name: "Nuwan", startsOn: "2026-09-22", endsOn: "2026-09-23" })],
    });
    // Once per day of the span, which is the point of a week grid.
    expect(screen.getAllByText("Nuwan").length).toBeGreaterThan(0);
  });
});

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
import MyWeekStrip from "./MyWeekStrip";
import {
  ANNUAL_LEAVE,
  EVENING,
  MONDAY,
  REGULAR,
  TZ,
  absence,
  assignment,
  shiftMap,
} from "../test/fixtures";

const SHIFTS = shiftMap(REGULAR, EVENING);

function renderStrip(over: Partial<React.ComponentProps<typeof MyWeekStrip>> = {}) {
  return render(
    <MyWeekStrip
      weekStart={MONDAY}
      mine={[]}
      everyone={[]}
      shifts={SHIFTS}
      tz={TZ}
      myAbsences={[]}
      absenceKinds={[ANNUAL_LEAVE]}
      {...over}
    />,
  );
}

describe("MyWeekStrip", () => {
  it("counts days rostered apart from days on a rotation", () => {
    // Regular hours is not a turn on the rota. Counting it as one told an
    // engineer on plain weekdays that they were on rotation five days out of
    // seven, which is the one distinction this page exists to draw.
    renderStrip({
      mine: [
        assignment({ name: "Asela", rotaDate: "2026-09-21", shiftCode: REGULAR.code }),
        assignment({ name: "Asela", rotaDate: "2026-09-22", shiftCode: REGULAR.code }),
        assignment({ name: "Asela", rotaDate: "2026-09-23", shiftCode: EVENING.code }),
      ],
    });
    const foot = screen.getByText(/days rostered this week/);
    expect(foot).toHaveTextContent("3");
    expect(foot).toHaveTextContent("1 on rotation");
  });

  it("says nothing is rostered when nothing is", () => {
    renderStrip();
    const foot = screen.getByText(/days rostered this week/);
    expect(foot).toHaveTextContent("0");
  });

  it("shows a day on leave as leave rather than as an empty day", () => {
    // A day on leave and a day with nothing on it read the same otherwise,
    // and they are not the same thing.
    renderStrip({
      myAbsences: [absence({ name: "Asela", startsOn: "2026-09-22", endsOn: "2026-09-22" })],
    });
    expect(screen.getAllByText("AL").length).toBeGreaterThan(0);
  });
});

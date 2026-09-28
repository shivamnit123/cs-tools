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

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import CellPicker, { type CellPickerTarget } from "./CellPicker";
import { ANNUAL_LEAVE, EVENING, LIEU_LEAVE, REGULAR, WEEKEND } from "../test/fixtures";

const ANCHOR = { top: 100, left: 100, bottom: 130, right: 144 };

function target(over: Partial<CellPickerTarget> = {}): CellPickerTarget {
  return {
    userId: "u1",
    name: "Asela",
    teamKey: "alpha",
    rotaDate: "2026-09-23", // a Wednesday
    anchor: ANCHOR,
    ...over,
  };
}

function renderPicker(over: {
  target?: CellPickerTarget;
  onApply?: () => void;
  onMarkAway?: () => void;
  onClear?: () => void;
} = {}) {
  const handlers = {
    onApply: over.onApply ?? vi.fn(),
    onMarkAway: over.onMarkAway ?? vi.fn(),
    onClear: over.onClear ?? vi.fn(),
    onClose: vi.fn(),
  };
  render(
    <CellPicker
      target={over.target ?? target()}
      shifts={[REGULAR, EVENING, WEEKEND]}
      leaveKinds={[ANNUAL_LEAVE, LIEU_LEAVE]}
      {...handlers}
    />,
  );
  return handlers;
}

describe("CellPicker: what it offers", () => {
  it("groups the rotations apart from the standing hours", () => {
    renderPicker();
    expect(screen.getByText("Rotations")).toBeInTheDocument();
    expect(screen.getByText("Standing hours")).toBeInTheDocument();
    expect(screen.getByText("Away")).toBeInTheDocument();
  });

  it("disables a weekend rotation on a weekday, and says why", () => {
    // The rule is shown where it applies rather than left for a lead to infer
    // from a window's absence.
    renderPicker();
    const weekendOption = screen.getByRole("button", { name: /Weekend rotation/ });
    expect(weekendOption).toBeDisabled();
    expect(weekendOption).toHaveTextContent("weekends only");
  });

  it("enables that same rotation on a Saturday", () => {
    renderPicker({ target: target({ rotaDate: "2026-09-26" }) });
    expect(screen.getByRole("button", { name: /Weekend rotation/ })).toBeEnabled();
  });

  it("marks the window the engineer already holds", () => {
    const { container } = render(
      <CellPicker
        target={target({ shiftCode: EVENING.code })}
        shifts={[REGULAR, EVENING, WEEKEND]}
        leaveKinds={[ANNUAL_LEAVE]}
        onApply={vi.fn()}
        onMarkAway={vi.fn()}
        onClear={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    const on = container.querySelector(".pk-c.on");
    expect(on).toHaveTextContent("Evening 6-9pm");
  });

  it("offers leave with no weekday rule of its own", () => {
    // Leave is stored as a span, so a weekend inside it is covered too.
    renderPicker();
    expect(screen.getByRole("button", { name: /Annual leave/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Lieu leave/ })).toBeEnabled();
  });
});

describe("CellPicker: the span it applies to", () => {
  it("starts closed on the day that was clicked", () => {
    renderPicker();
    expect(screen.getByText("1 day")).toBeInTheDocument();
  });

  it("counts the days as the end moves", () => {
    renderPicker();
    fireEvent.change(screen.getByLabelText("Mark until"), { target: { value: "2026-09-25" } });
    expect(screen.getByText("3 days")).toBeInTheDocument();
  });

  it("treats an end before the start as no end chosen yet", () => {
    // A date input reports a whole date per segment, so the first digit of a
    // two-digit day is briefly an earlier date. Acting on it would apply a
    // backwards range.
    renderPicker();
    fireEvent.change(screen.getByLabelText("Mark until"), { target: { value: "2026-09-02" } });
    expect(screen.getByText("1 day")).toBeInTheDocument();
  });

  it("treats a year the field will accept but no one meant as no end either", () => {
    // The year segment takes six digits, so "202609-02-09" is a value this
    // input genuinely hands over. A plain string comparison let it through
    // and the day count read "NaN days".
    renderPicker();
    fireEvent.change(screen.getByLabelText("Mark until"), { target: { value: "202609-02-09" } });
    expect(screen.getByText("1 day")).toBeInTheDocument();
    expect(screen.queryByText(/NaN/)).not.toBeInTheDocument();
  });

  it("applies the window over the whole span", () => {
    const { onApply } = renderPicker();
    fireEvent.change(screen.getByLabelText("Mark until"), { target: { value: "2026-09-25" } });
    fireEvent.click(screen.getByRole("button", { name: /Evening 6-9pm/ }));
    expect(onApply).toHaveBeenCalledWith(EVENING.code, "2026-09-23", "2026-09-25");
  });

  it("marks leave over the whole span", () => {
    const { onMarkAway } = renderPicker();
    fireEvent.change(screen.getByLabelText("Mark until"), { target: { value: "2026-09-25" } });
    fireEvent.click(screen.getByRole("button", { name: /Annual leave/ }));
    expect(onMarkAway).toHaveBeenCalledWith("ANNUAL_LEAVE", "2026-09-23", "2026-09-25");
  });

  it("warns that a span will skip the days a rotation is not worked on", () => {
    renderPicker();
    expect(screen.queryByText(/are skipped/)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Mark until"), { target: { value: "2026-09-29" } });
    expect(screen.getByText(/are skipped/)).toBeInTheDocument();
  });
});

describe("CellPicker: what clearing means here", () => {
  it("puts a weekday back on the standing window the engineer already holds", () => {
    const { onClear } = renderPicker({
      target: target({ baseShiftCode: REGULAR.code }),
    });
    const clear = screen.getByRole("button", { name: /Clear/ });
    expect(clear).toHaveTextContent("Regular hours");
    fireEvent.click(clear);
    expect(onClear).toHaveBeenCalledWith(REGULAR.code, "2026-09-23", "2026-09-23");
  });

  it("empties a weekend, which has no standing window to fall back to", () => {
    const { onClear } = renderPicker({
      target: target({ rotaDate: "2026-09-26", baseShiftCode: REGULAR.code }),
    });
    fireEvent.click(screen.getByRole("button", { name: /Clear/ }));
    expect(onClear).toHaveBeenCalledWith("", "2026-09-26", "2026-09-26");
  });

  it("brings somebody marked away back on the rota instead", () => {
    // The rota underneath was covered, not deleted, so it shows through
    // again; rewriting the assignment would churn the history for a change
    // that did not happen.
    const { onClear } = renderPicker({
      target: target({ absenceKindCode: "ANNUAL_LEAVE", baseShiftCode: REGULAR.code }),
    });
    const clear = screen.getByRole("button", { name: /Clear/ });
    expect(clear).toHaveTextContent("back on the rota");
    fireEvent.click(clear);
    expect(onClear).toHaveBeenCalledWith(REGULAR.code, "2026-09-23", "2026-09-23");
  });
});

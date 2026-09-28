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

import {
  Fragment,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type JSX,
} from "react";
import type { ScheduleAbsenceKind, ScheduleShift } from "../types";


export interface CellPickerTarget {
  userId: string;
  name: string;
  teamKey: string;
  rotaDate: string;
  /** What they currently hold that day, for marking the live code. */
  shiftCode?: string;
  /** The leave or allocation covering that day, if any, marked the same way. */
  absenceKindCode?: string;
  /** The zone column this was opened from, on a day split across them. */
  zoneCode?: string;
  /** Where on screen the cell is, so the picker can sit against it. */
  anchor: { top: number; left: number; bottom: number; right: number };
  /** The standing window this engineer sits in on an ordinary weekday, so
   *  clearing a weekday puts them back on it rather than leaving a hole.
   *  Absent when they hold no standing window to go back to. */
  baseShiftCode?: string;
}

interface CellPickerProps {
  target: CellPickerTarget;
  /** The windows this group can be put on, already filtered to CRE or SRE. */
  shifts: ScheduleShift[];
  /** The reasons a lead may mark somebody away -- annual and lieu leave.
   *  Allocations are deliberately not here: who is on a customer engagement is
   *  not the ABT lead's call to make from a rota grid. */
  leaveKinds: ScheduleAbsenceKind[];
  onApply: (shiftCode: string, from: string, to: string) => void;
  /** Mark them away over the span, for a leave kind rather than a window. */
  onMarkAway: (kindCode: string, from: string, to: string) => void;
  /** Clear the span: bring them back if they are marked away, otherwise put
   *  them back on the standing window -- or, with no code, which is what a
   *  weekend clear sends, take them off entirely. */
  onClear: (shiftCode: string, from: string, to: string) => void;
  onClose: () => void;
  busy?: boolean;
}

/** Is this window worked on this day at all? `day_scope` already knows. */
function allowedOn(shift: ScheduleShift, iso: string): boolean {
  const d = new Date(`${iso}T00:00:00`);
  const weekend = d.getDay() === 0 || d.getDay() === 6;
  if (shift.dayScope === "WEEKEND") return weekend;
  if (shift.dayScope === "WEEKDAY") return !weekend;
  return true;
}

/** Why a window is greyed out, in the words the grid already uses. */
function notWorked(shift: ScheduleShift): string {
  return shift.dayScope === "WEEKEND" ? "weekends only" : "weekdays only";
}

function isWeekendIso(iso: string): boolean {
  const d = new Date(`${iso}T00:00:00`);
  return d.getDay() === 0 || d.getDay() === 6;
}

function dayCount(from: string, to: string): number {
  const a = new Date(`${from}T00:00:00`).getTime();
  const b = new Date(`${to}T00:00:00`).getTime();
  return Math.max(1, Math.round((b - a) / 86_400_000) + 1);
}

/**
 * The picker a lead gets on a roster cell.
 *
 * Anchored to the cell rather than opened as a modal: the point of this grid is
 * the rows around the one being changed -- who else is on that week, who is
 * already away -- and a dialog in the middle of the screen hides exactly that.
 *
 * The caller keys it on the cell, so a new cell is a new decision: the range
 * starts closed again rather than carrying the last one over onto somebody
 * else, without an effect reaching in to reset it.
 *
 * It edits a span, not a day. "On the evening rotation all next week" is one
 * decision, and making somebody click it five times is how a rota ends up half
 * changed.
 */
export default function CellPicker({
  target,
  shifts,
  leaveKinds,
  onApply,
  onMarkAway,
  onClear,
  onClose,
  busy,
}: CellPickerProps): JSX.Element {
  const [until, setUntil] = useState(target.rotaDate);
  const box = useRef<HTMLDivElement | null>(null);
  const [at, setAt] = useState<{ left: number; top: number } | null>(null);


  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") onClose();
    };
    const onDown = (e: MouseEvent): void => {
      if (box.current && !box.current.contains(e.target as Node)) onClose();
    };
    window.addEventListener("keydown", onKey);
    // Deferred to the next tick: the click that opened the picker is still
    // propagating, and would otherwise close it immediately.
    const t = window.setTimeout(() => document.addEventListener("mousedown", onDown), 0);
    return () => {
      window.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDown);
      window.clearTimeout(t);
    };
  }, [onClose]);

  /** The end the range really has.
   *
   *  A date still being typed is not a date yet: it reads as earlier than the
   *  start for a keystroke or two, and a year segment accepts six digits, so
   *  "202609-02-09" is a value this field will genuinely hand over. Both are
   *  taken as "no end chosen yet" rather than acted on -- without the shape
   *  check the day count reads "NaN days" and the write would carry the same
   *  nonsense to the server. */
  const lastDay =
    /^\d{4}-\d{2}-\d{2}$/.test(until) &&
    !Number.isNaN(Date.parse(`${until}T00:00:00`)) &&
    until >= target.rotaDate
      ? until
      : target.rotaDate;
  const span = dayCount(target.rotaDate, lastDay);
  /** Leave is not marked on a weekend -- see the Away buttons below. */
  const onWeekend = isWeekendIso(target.rotaDate);

  /** Rotations first under their own heading, then the standing windows,
   *  each marked with whether it is worked on the day the picker opened on. */
  const groups = useMemo(() => {
    const decorate = (list: ScheduleShift[]) =>
      [...list]
        .sort((a, b) => a.sortOrder - b.sortOrder)
        .map((s) => ({ shift: s, ok: allowedOn(s, target.rotaDate) }));
    return [
      { heading: "Rotations", items: decorate(shifts.filter((s) => s.isRotation)) },
      { heading: "Standing hours", items: decorate(shifts.filter((s) => !s.isRotation)) },
    ].filter((g) => g.items.length > 0);
  }, [shifts, target.rotaDate]);
  const groupCount = groups.reduce((n, g) => n + g.items.length, 0);

  /** Sit under the cell, or above it when there is no room below, and never
   *  off the side. Measured after the first paint because the height depends
   *  on how many windows this group runs. */
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const m = 12;
    const w = el.offsetWidth;
    const h = el.offsetHeight;
    const left = Math.min(Math.max(m, target.anchor.left), window.innerWidth - w - m);
    let top = target.anchor.bottom + 8;
    if (top + h > window.innerHeight - m) top = Math.max(m, target.anchor.top - h - 8);
    if (top + h > window.innerHeight - m) top = Math.max(m, window.innerHeight - h - m);
    setAt({ left: Math.round(left), top: Math.round(top) });
  }, [target.anchor, groupCount]);

  /** What clearing means here. A weekend has no standing window to fall back
   *  to, so clearing it genuinely empties the day. */
  const weekendDay = (() => {
    const d = new Date(`${target.rotaDate}T00:00:00`).getDay();
    return d === 0 || d === 6;
  })();
  const backTo = weekendDay ? "" : (target.baseShiftCode ?? "");

  const when = new Date(`${target.rotaDate}T00:00:00`).toLocaleDateString("en-GB", {
    weekday: "short",
    day: "numeric",
    month: "short",
  });

  return (
    <div
      className="picker open"
      ref={box}
      role="dialog"
      aria-label={`Change ${target.name}'s rota`}
      // Off screen until it has been measured, rather than at 0,0 where it
      // would flash in the corner for a frame before landing on the cell.
      style={at ?? { top: -9999, left: -9999 }}
    >
      <div className="pk-head">
        <b>{target.name}</b>
        <span className="pk-d">{when}</span>
        <button type="button" className="pk-x" onClick={onClose} aria-label="Close">
          &times;
        </button>
      </div>

      <label className="pk-range">
        <span>
          From <b>{when}</b> until
        </span>
        <input
          type="date"
          // Uncontrolled on purpose. A date input keeps its own half-typed
          // segment, and feeding `value` back on every change throws that
          // away: the "2" of "29" lands as the 2nd, React re-renders, and the
          // "9" starts over as the 9th. The date is read out of the field
          // instead, and the component is keyed on the cell so it still
          // starts fresh when another cell is picked.
          defaultValue={target.rotaDate}
          min={target.rotaDate}
          // Taken as typed, and only settled back to the start once focus
          // leaves. Clamping on every change cannot work here: a date input
          // reports a whole date per segment, so the first digit of "25" is
          // the 2nd of the month, which is before the start -- snapping there
          // resets the field under the typist and the second digit can never
          // land. What the range actually means is clamped below instead, so
          // nothing downstream sees a backwards span.
          onChange={(e) => setUntil(e.target.value || target.rotaDate)}
          // Settled back into the field once focus leaves, so a date left
          // before the start does not sit there disagreeing with the day
          // count beside it.
          onBlur={(e) => {
            e.target.value = lastDay;
            setUntil(lastDay);
          }}
          aria-label="Mark until"
        />
        <span className="pkr-n">
          {span} day{span === 1 ? "" : "s"}
        </span>
      </label>

      <div className="pk-grid">
        {groups.map((group) => (
          <Fragment key={group.heading}>
            <div className="pk-sec">{group.heading}</div>
            {group.items.map(({ shift, ok }) => (
              <button
                key={shift.code}
                type="button"
                className={`pk-c ${shift.code === target.shiftCode ? "on" : ""} ${ok ? "" : "bad"}`}
                disabled={!ok || busy}
                title={ok ? shift.label : `${shift.label} — ${notWorked(shift)}`}
                onClick={() => onApply(shift.code, target.rotaDate, lastDay)}
              >
                <span className={`chip sm ${shift.colourToken}`}>{shift.shortCode}</span>
                {/* A window that does not apply today still says what it is.
                    "weekends only" on its own left a lead reading chips to
                    work out which weekend window was which. */}
                <span className="pk-l">
                  {shift.label}
                  {ok ? null : <small className="pk-n">{notWorked(shift)}</small>}
                </span>
              </button>
            ))}
          </Fragment>
        ))}

        {leaveKinds.length > 0 ? (
          <>
            <div className="pk-sec">Away</div>
            {leaveKinds.map((kind) => (
              <button
                key={kind.code}
                type="button"
                className={`pk-c ${kind.code === target.absenceKindCode ? "on" : ""}${
                  onWeekend && kind.bucket === "LEAVE" ? " bad" : ""
                }`}
                // No leave on a weekend: nobody is rostered to be away from a
                // Saturday. A span that starts on a weekday may still run
                // across one; the grid simply leaves those days alone.
                disabled={busy || (onWeekend && kind.bucket === "LEAVE")}
                // No weekday rule here, unlike a rotation. Leave is stored as a
                // span rather than a day at a time, so a weekend inside it is
                // covered too -- somebody away Friday to Monday is away for the
                // weekend, and skipping it would say they were back for two
                // days in the middle of their own leave.
                title={`${kind.label} — the whole span, weekends included`}
                onClick={() => onMarkAway(kind.code, target.rotaDate, lastDay)}
              >
                <span className={`chip sm ${kind.colourToken}`}>{kind.shortCode}</span>
                <span className="pk-l">{kind.label}</span>
              </button>
            ))}
          </>
        ) : null}

        <button
          type="button"
          className="pk-c clear"
          disabled={busy}
          onClick={() => onClear(backTo, target.rotaDate, lastDay)}
        >
          {/* Says what clearing will actually do here, which is not the same
              thing in the three cases. Somebody marked away comes back onto
              whatever the rota already had for them; a weekend has no standing
              window to fall back to at all. */}
          {target.absenceKindCode
            ? "Clear — back on the rota"
            : backTo
              ? `Clear — back to ${shifts.find((s) => s.code === backTo)?.label ?? "regular hours"}`
              : "Clear — nothing rostered"}
        </button>
      </div>

      {/* Over a span, a weekday rotation will skip the weekend inside it, and
          the reader should know that before they click rather than after. */}
      {span > 1 ? (
        <p className="pk-note">
          Days a rotation is not worked on are skipped — a weekday rotation over
          a week sets the five weekdays.
        </p>
      ) : null}
    </div>
  );
}

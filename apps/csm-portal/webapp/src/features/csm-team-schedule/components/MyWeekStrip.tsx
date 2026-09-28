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

import { useMemo, useRef, useState, type JSX } from "react";
import { Users } from "@wso2/oxygen-ui-icons-react";
import type {
  ScheduleAbsence,
  ScheduleAbsenceKind,
  ScheduleAssignment,
  ScheduleShift,
} from "../types";
import { addDays, groupBy, initialsOf, shortDayName, timeOf, toIsoDate , isPeerRotation } from "../utils/rota";
import { accentOf } from "../utils/rotaHues";
import { useTeamColour } from "../utils/teamColourContext";

interface MyWeekStripProps {
  weekStart: Date;
  /** The signed-in engineer's own rota for the week. */
  mine: ScheduleAssignment[];
  /** Everyone's rota for the week, for the day a reader opens. */
  everyone: ScheduleAssignment[];
  shifts: Map<string, ScheduleShift>;
  tz: string;
  /** The engineer's own leave and allocations over the week. Without them a
   *  day on leave, or lent to a customer, read "—" -- the same as a day with
   *  nothing on it, which is the one thing it is not. */
  myAbsences?: ScheduleAbsence[];
  absenceKinds?: ScheduleAbsenceKind[];
}

/** How long the cursor must rest on a day before it opens, so sweeping across
 *  the strip does not fire seven times. */
const HOVER_DELAY_MS = 110;

/**
 * My week: seven day cards, and the day the cursor rests on opens underneath
 * with everyone who is on the rota that day.
 *
 * It opens on hover and does not close on the way out -- a reader who just
 * opened a day is almost always heading down to read it, and closing it under
 * them would snatch it away mid-move. Clicking the open day closes it.
 *
 * That a card opens at all has to be visible before anyone hovers: a cue that
 * only appears on hover teaches nothing, and on a touch screen there is no
 * hover. So every card carries a "Who's on" cue at rest, and until a day is
 * opened the space the list will fill says what to do.
 */
export default function MyWeekStrip({
  weekStart,
  mine,
  everyone,
  shifts,
  tz,
  myAbsences = [],
  absenceKinds = [],
}: MyWeekStripProps): JSX.Element {
  const [openDay, setOpenDay] = useState<string | null>(null);
  const timer = useRef<number | null>(null);

  const days = useMemo(
    () => Array.from({ length: 7 }, (_, i) => addDays(weekStart, i)),
    [weekStart],
  );
  const mineByDay = useMemo(() => groupBy(mine, (a) => a.rotaDate), [mine]);
  const kindByCode = useMemo(() => new Map(absenceKinds.map((k) => [k.code, k])), [absenceKinds]);
  /** The absence covering a day, if any. An open-ended one (no end date) is a
   *  standing allocation and covers every day from its start. */
  const absenceOn = (iso: string): ScheduleAbsence | undefined =>
    myAbsences.find((ab) => ab.startsOn <= iso && (!ab.endsOn || iso <= ab.endsOn));
  const todayIso = toIsoDate(new Date());

  const hoverOpen = (iso: string): void => {
    if (openDay === iso) return;
    if (timer.current) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setOpenDay(iso), HOVER_DELAY_MS);
  };
  const cancelHover = (): void => {
    if (timer.current) window.clearTimeout(timer.current);
  };

  // Only the rotations this reader could be sharing: not regular hours, which
  // on a weekday is most of the team, and not the Americas night cover, which
  // is that team's own standing shift rather than a turn in the rota.
  const openRows = openDay
    ? everyone.filter((a) => a.rotaDate === openDay && isPeerRotation(shifts.get(a.shiftCode)))
    : [];
  // Two different counts. "Rostered" is any day with something on it, regular
  // hours included; "on rotation" is only a turn on the rota. Counting the first
  // under the second's name told an engineer on plain regular hours all week
  // that they were on rotation five days out of seven.
  const rosteredCount = days.filter((d) => (mineByDay.get(toIsoDate(d)) ?? []).length > 0).length;
  const onRotaCount = days.filter((d) =>
    (mineByDay.get(toIsoDate(d)) ?? []).some((a) => isPeerRotation(shifts.get(a.shiftCode))),
  ).length;

  return (
    <>
      <div className="strip">
        {days.map((d) => {
          const iso = toIsoDate(d);
          const rows = mineByDay.get(iso) ?? [];
          const first = rows[0];
          const shift = first ? shifts.get(first.shiftCode) : undefined;
          const past = iso < todayIso;
          const weekend = d.getDay() === 0 || d.getDay() === 6;
          // A weekend inside a span of leave or an allocation reads as off:
          // leave is not taken on a weekend, and nobody works an engagement
          // on a Saturday.
          const absence = weekend ? undefined : absenceOn(iso);

          return (
            <div
              key={iso}
              className={[
                "dayc",
                weekend ? "wknd" : "",
                iso === todayIso ? "today" : "",
                past ? "past" : "",
                rows.length ? "rot" : "",
                openDay === iso ? "picked" : "",
              ]
                .filter(Boolean)
                .join(" ")}
              style={{ ["--rc" as string]: accentOf(shift?.colourToken ?? "lk") }}
              role="button"
              tabIndex={0}
              aria-expanded={openDay === iso}
              aria-controls="mywk-peek"
              aria-label={`${d.toDateString()}: show everyone on rotation`}
              onMouseEnter={() => hoverOpen(iso)}
              onMouseLeave={cancelHover}
              onFocus={() => setOpenDay(iso)}
              onClick={() => setOpenDay(openDay === iso ? null : iso)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  setOpenDay(openDay === iso ? null : iso);
                }
              }}
            >
              {iso === todayIso ? <span className="daytag">Today</span> : null}
              <span className="daysee" aria-hidden="true">
                <Users size={11} />
                <span className="dslabel">{openDay === iso ? "Hide" : "Who's on"}</span>
              </span>
              <span className="dw">{shortDayName(d)}</span>
              <span className="dn tn">{d.getDate()}</span>
              {absence ? (
                // Leave or an allocation wins the day, as it does on the
                // roster: someone away is not on the rota, whatever a
                // generated row says.
                <>
                  <span
                    className={`chip ${kindByCode.get(absence.kindCode)?.colourToken ?? ""}`}
                    title={[kindByCode.get(absence.kindCode)?.label, absence.allocatedTo].filter(Boolean).join(" · ")}
                  >
                    {kindByCode.get(absence.kindCode)?.shortCode ?? absence.kindCode}
                  </span>
                  <span className="tm">
                    {absence.allocatedTo ?? kindByCode.get(absence.kindCode)?.label ?? ""}
                  </span>
                </>
              ) : first && shift ? (
                <>
                  <span className={`chip ${shift.colourToken}`}>{shift.shortCode}</span>
                  <span className="tm">
                    {timeOf(first.startsAt, tz)} – {timeOf(first.endsAt, tz)}
                  </span>
                </>
              ) : (
                <span className="tm">{weekend ? "Off" : "—"}</span>
              )}
            </div>
          );
        })}
      </div>

      {openDay ? (
        <div className="peek" id="mywk-peek">
          <div className="ph">
            <b>
              {new Date(`${openDay}T00:00:00`).toLocaleDateString(undefined, {
                weekday: "long",
                day: "numeric",
                month: "long",
                year: "numeric",
              })}
            </b>
            {openDay === todayIso ? <span className="tag today">Today</span> : null}
            <span className="count">{openRows.length}</span>
            <span className="sub">on rotation</span>
            <button className="pk-x" onClick={() => setOpenDay(null)} aria-label="Close">
              ×
            </button>
          </div>
          <PeekRows rows={openRows} shifts={shifts} />
        </div>
      ) : (
        <div className="peek peekhint" id="mywk-peek">
          <Users size={16} />
          <span>
            <b>See who's on rotation with you.</b> Hover over a day above, or tap it, to list
            everyone rostered that day.
          </span>
        </div>
      )}

      <div className="wkstat">
        <span className="kv">
          <b>{rosteredCount}</b> of 7 days rostered this week · <b>{onRotaCount}</b> on rotation
        </span>
        {openDay ? (
          <span className="grp hint">Click the open day again, or ×, to close it</span>
        ) : null}
      </div>
    </>
  );
}

/** Everyone on the opened day, grouped by the rotation they are on. */
function PeekRows({
  rows,
  shifts,
}: {
  rows: ScheduleAssignment[];
  shifts: Map<string, ScheduleShift>;
}): JSX.Element {
  const teamColourOf = useTeamColour();
  const byShift = useMemo(() => {
    const groups = [...groupBy(rows, (r) => r.shiftCode).entries()];
    return groups.sort(
      (a, b) => (shifts.get(a[0])?.sortOrder ?? 999) - (shifts.get(b[0])?.sortOrder ?? 999),
    );
  }, [rows, shifts]);

  if (rows.length === 0) {
    return <div className="offnone">Nobody is on the rota that day.</div>;
  }

  return (
    <div className="peekgrid">
      {byShift.map(([code, list]) => {
        const shift = shifts.get(code);
        return (
          <div className="pg" key={code}>
            <h5>
              <span className={`chip sm ${shift?.colourToken ?? ""}`}>
                {shift?.label ?? code}
              </span>
              <span className="count">{list.length}</span>
            </h5>
            {list.map((a) => (
              <div className="nm" key={a.id}>
                <span className="av" style={{ background: teamColourOf(a.teamKey) }}>{initialsOf(a.engineer.name)}</span>
                <span className="who">{a.engineer.name}</span>
                {a.engineer.isLead ? <span className="tag lead-t">Lead</span> : null}
                <span className="team">{a.teamKey}</span>
              </div>
            ))}
          </div>
        );
      })}
    </div>
  );
}

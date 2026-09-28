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

import { useMemo, useState, type JSX } from "react";
import QueryErrorState from "@components/QueryErrorState";
import { useErrorBanner } from "@context/error-banner/ErrorBannerContext";
import { useCurrentUser } from "@context/current-user/CurrentUserContext";
import {
  useApplyAbsence,
  useScheduleEditMarkers,
  useApplyRange,
  useMyLeadTeams,
  useScheduleAbsences,
  useScheduleAbsencesByMonth,
  useScheduleAssignments,
  useScheduleAssignmentsByMonth,
  useScheduleCatalogue,
  useTeamActivity,
  type MonthWindow,
  type RotaRead,
} from "../api/useTeamSchedule";
import CellPicker, { type CellPickerTarget } from "../components/CellPicker";
import DayLadder, { type LadderLane } from "../components/DayLadder";
import MonthRoster from "../components/MonthRoster";
import MyWeekStrip from "../components/MyWeekStrip";
import NextRotation from "../components/NextRotation";
import RecentChanges from "../components/RecentChanges";
import WeekTable from "../components/WeekTable";
import type {
  ScheduleAbsencesResponse,
  ScheduleAssignment,
  ScheduleAssignmentsResponse,
} from "../types";
import { resolveDisplayTimeZone } from "@utils/dateTime";
import { TeamColourProvider } from "../utils/teamColour";
import {
  addDays,
  isRotationShift,
  mondayOf,
  shiftsByCode,
  toIsoDate,
  zoneAbbreviation,
} from "../utils/rota";
import { zoneColour } from "../utils/rotaHues";
import { SCHEDULE_THEME_VARS } from "../utils/useScheduleTheme";
import "../teamSchedule.css";

type ViewTab = "mine" | "today" | "week" | "roster";
type Family = "CRE" | "SRE";

// The team list used to live here, mirroring CSM_TEAM_REGISTRY. Team names
// are organisation vocabulary and committing them coupled this page to a
// deploy it cannot see, which is the coupling that registry exists to avoid.
// The catalogue serves them now.

/** How far ahead to look for the reader's next rotation. Eight weeks covers
 *  every rotation in the cycle without asking the API for a year of rows. */
const NEXT_ROTATION_HORIZON_DAYS = 56;

const TITLE: Record<ViewTab, string> = {
  mine: "My week",
  today: "Who is working today",
  week: "Who is working this week",
  roster: "Month roster",
};

/** How many months the roster shows either side of the selected one. One:
 *  last month, this month and next -- the history a lead checks a swap
 *  against, and the month they are planning, without paging. */
const ROSTER_MONTHS_EITHER_SIDE = 1;

const fmtMonthRange = (first: Date, last: Date): string => {
  const sameYear = first.getFullYear() === last.getFullYear();
  const a = first.toLocaleDateString(undefined, sameYear ? { month: "short" } : { month: "short", year: "numeric" });
  const b = last.toLocaleDateString(undefined, { month: "short", year: "numeric" });
  return `${a} – ${b}`;
};

const fmtLong = (d: Date): string =>
  d.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" });

/** Previous/Next moves by whatever the tab is about: a day, a week, a month. */
function stepBy(from: Date, tab: ViewTab, direction: 1 | -1): Date {
  if (tab === "today") return addDays(from, direction);
  if (tab === "roster") {
    const next = new Date(from);
    next.setDate(1);
    next.setMonth(next.getMonth() + direction);
    return next;
  }
  return addDays(from, 7 * direction);
}

/** A change made in the current editing session. */
interface RotaChange {
  userId: string;
  name: string;
  from: string;
  to: string;
  /** What the days became: a window's label, a kind of leave, or back on the rota. */
  what: string;
}

/** "Thu 1 Oct", or "Thu 1 – Sat 3 Oct" for a span. */
function fmtRange(from: string, to: string): string {
  const f = new Date(`${from}T00:00:00`);
  const opts = { weekday: "short", day: "numeric", month: "short" } as const;
  if (from === to) return f.toLocaleDateString(undefined, opts);
  return `${f.toLocaleDateString(undefined, { weekday: "short", day: "numeric" })} – ${new Date(
    `${to}T00:00:00`,
  ).toLocaleDateString(undefined, opts)}`;
}

const fmtShort = (d: Date): string =>
  d.toLocaleDateString(undefined, { day: "numeric", month: "short" });

/**
 * Team Schedule: who from CRE and SRE is working, when, and in which
 * escalation tier.
 */
export default function CsmTeamSchedulePage(): JSX.Element {
  /** null until the reader picks a tab, so the default can follow who they
   *  are -- see `tab` below. */
  const [tabChoice, setTab] = useState<ViewTab | null>(null);
  /** null until the reader picks one, so their own group can be the default
   *  once the profile arrives. Derived rather than corrected in an effect: an
   *  SRE engineer must never render a frame on CRE, because the tab gating
   *  would show them three disabled tabs on arrival. */
  const [familyChoice, setFamilyChoice] = useState<Family | null>(null);
  const [teamKey, setTeamKey] = useState<string>("");
  const [anchor, setAnchor] = useState<Date>(() => new Date());
  /** Bumped by Today; a view listens to it to re-centre on the current day. */
  const [focusRequest, setFocusRequest] = useState(0);

  const { user } = useCurrentUser();
  // The clock this reader is on, and the only source of it: their CSM profile
  // timezone, through the portal's own resolver. Set it there once and every
  // view follows, including after a move from Colombo to San Francisco.
  //
  // There is deliberately no picker on this page. A second control could
  // disagree with the profile, and then two people comparing the same rota
  // over a call have no way of knowing whose clock they are each reading.
  const tz = resolveDisplayTimeZone(user?.timeZone);
  const catalogue = useScheduleCatalogue();

  /** The teams of the group on screen, in the order the catalogue gives them.
   *  Empty until it loads, which reads as "no teams yet" rather than as a
   *  wrong list. */
  const teamsOf = useMemo(() => {
    const all = catalogue.data?.teams ?? [];
    return (f: Family): string[] =>
      all.filter((t) => t.family === f).map((t) => t.key);
  }, [catalogue.data?.teams]);

  // Which teams this reader may edit. Asked once: it changes when somebody is
  // made a lead, not while they are looking at a rota.
  const leadTeams = useMyLeadTeams();
  const { showError } = useErrorBanner();
  const applyRange = useApplyRange();
  const applyAbsence = useApplyAbsence();

  /** Edit mode, and the cell it has open.
   *
   *  A mode rather than always-on, and owned here rather than by the roster,
   *  because the toggle belongs with the other page controls that change what
   *  a click does. Turning editing off closes whatever is open with it. */
  const [editing, setEditing] = useState(false);
  const [picker, setPicker] = useState<CellPickerTarget | null>(null);

  /** The changes made in this editing session, newest last.
   *
   *  A change saves the moment it is made, and the cell then simply shows its
   *  new value -- which looks exactly like a value that was always there. So
   *  a lead who has changed six days could not see which six. Each saved
   *  change is kept here and marked on the roster until they click Done
   *  editing, which is the point they have said they are finished. */
  const [changes, setChanges] = useState<RotaChange[]>([]);
  /** Whether the lead has the recent-changes panel open on the roster. */
  const [showRecent, setShowRecent] = useState(false);
  const changedCells = useMemo(() => {
    const out = new Set<string>();
    for (const c of changes) {
      for (let d = new Date(`${c.from}T00:00:00`); toIsoDate(d) <= c.to; d = addDays(d, 1)) {
        out.add(`${c.userId}|${toIsoDate(d)}`);
      }
    }
    return out;
  }, [changes]);

  /** Whether this reader leads anything, which is what decides whether the
   *  toggle is offered. Whether a given row is theirs is a separate question
   *  the roster answers per team, and the server answers again on the write. */
  const canEditRota = (leadTeams.data ?? []).length > 0;


  /** The reader's own group, from their CSM profile. Absent for anyone who
   *  belongs to no team -- a manager -- which is why it is optional rather
   *  than defaulting to CRE. */
  const myFamily: Family | undefined = useMemo(() => {
    const f = user?.team?.family?.toUpperCase();
    if (!f) return undefined;
    return f.startsWith("SRE") ? "SRE" : "CRE";
  }, [user?.team?.family]);

  /** The group on screen: the reader's own until they choose otherwise.
   *  CRE only as a last resort, for a manager who belongs to neither. */
  const family: Family = familyChoice ?? myFamily ?? "CRE";

  /** Their own group first in the segment. An SRE engineer reads "SRE | CRE",
   *  because the first thing in a pair reads as the default, and theirs is. */
  const families: Family[] =
    myFamily === "SRE" ? ["SRE", "CRE"] : ["CRE", "SRE"];

  /** Nobody's group: a manager, who belongs to no team at all. */
  const isManager = myFamily === undefined;

  /** The tab the page opens on. An engineer's first question is their own
   *  rota, so they land on My week; a manager holds none, and comes here to see
   *  who is covering, so they land on Today. Derived rather than set in an
   *  effect, like `family` above: /users/me has settled before this page
   *  mounts, so the first frame is already the right one. */
  const tab: ViewTab = tabChoice ?? (isManager ? "today" : "mine");

  /** Whether the group on screen is the reader's own. */
  const ownGroup = !isManager && myFamily === family;

  /**
   * Which views apply to this reader, on this group.
   *
   *   own group     all four -- their rota, their team, their month
   *   other group   Today only. It answers "who is covering right now", which
   *                 a CRE engineer escalating to SRE needs. This week and the
   *                 roster are planning views for a rota they are not part of.
   *   a manager     everything but My week, on both groups. The one thing
   *                 that genuinely has nothing to show them is their own rota,
   *                 because they hold none. Who is covering -- today, across
   *                 the week, across the month -- is their question for CRE
   *                 and SRE alike, so the roster is theirs to read too.
   */
  const appliesToView = (t: ViewTab): boolean => {
    if (isManager) return t !== "mine";
    return ownGroup || t === "today";
  };

  /**
   * Which tabs are on the strip at all.
   *
   * Disabling and removing answer different situations. An engineer looking at
   * the other group sees the tab greyed, because toggling back brings it
   * straight back -- removing it would make the strip change shape under them.
   * A manager has no such toggle: they hold no rota on either group, so My week
   * can never apply to them, and a permanently dead tab is just something to
   * wonder about. It is not offered.
   */
  const visibleTabs: ViewTab[] = (["mine", "today", "week", "roster"] as ViewTab[]).filter(
    (t) => !(isManager && t === "mine"),
  );

  /** The tab actually being shown.
   *
   *  Derived, not corrected after the fact: switching group while on My week
   *  has to land somewhere the same render, or the reader sees one frame of an
   *  empty strip before it rights itself. Their choice of tab is remembered,
   *  so switching back to their own group returns them to My week. */
  const view: ViewTab = appliesToView(tab) ? tab : "today";

  const weekStart = useMemo(() => mondayOf(anchor), [anchor]);
  /** The group/team controls the cards render in their own heads. It is the
   *  page's state either way -- the toolbar and the card head are two views of
   *  one control, which is why they can never disagree. */
  const scopeControls = {
    family,
    onFamilyChange: (f: Family) => {
      setFamilyChoice(f);
      setTeamKey("");
    },
    teamKey,
    onTeamKeyChange: setTeamKey,
    teams: teamsOf(family),
    families,
  };

  const dayView = view === "today";
  const rosterView = view === "roster";
  /** The roster's window: the selected month with one either side, as the
   *  calendar months it is fetched in. Keyed on the month rather than the
   *  anchor, so stepping a day inside a month does not rebuild it. */
  const anchorMonthKey = `${anchor.getFullYear()}-${anchor.getMonth()}`;
  const rosterMonths: MonthWindow[] = useMemo(() => {
    const [y, m] = anchorMonthKey.split("-").map(Number);
    const out: MonthWindow[] = [];
    for (let o = -ROSTER_MONTHS_EITHER_SIDE; o <= ROSTER_MONTHS_EITHER_SIDE; o++) {
      out.push({ from: toIsoDate(new Date(y, m + o, 1)), to: toIsoDate(new Date(y, m + o + 1, 0)) });
    }
    return out;
  }, [anchorMonthKey]);
  const rosterStart = useMemo(() => new Date(`${rosterMonths[0].from}T00:00:00`), [rosterMonths]);
  const rosterEnd = useMemo(
    () => new Date(`${rosterMonths[rosterMonths.length - 1].to}T00:00:00`),
    [rosterMonths],
  );
  const from = dayView ? toIsoDate(anchor) : toIsoDate(weekStart);
  const to = dayView ? toIsoDate(anchor) : toIsoDate(addDays(weekStart, 6));
  const teamKeys = teamKey ? [teamKey] : undefined;

  const singleRead = useScheduleAssignments(
    {
      from,
      to,
      family,
      teamKeys,
      // Only the day view needs the crew that started last night and is still
      // working this morning; the week groups by rota date, so it does not.
      includeOvernight: dayView,
    },
    !rosterView,
  );
  // The roster spans three months, which is more than one read may ask for,
  // so it is read a month at a time and merged.
  const rosterRead = useScheduleAssignmentsByMonth({ family, teamKeys }, rosterMonths, rosterView);
  const assignments: RotaRead<ScheduleAssignmentsResponse> = rosterView ? rosterRead : singleRead;

  // My week is the signed-in engineer's own rota, found by the email the
  // portal knows them by.
  const mine = useScheduleAssignments(
    { from: toIsoDate(weekStart), to: toIsoDate(addDays(weekStart, 6)), userEmail: user?.email ?? "" },
    view === "mine" && Boolean(user?.email),
  );

  // Their own leave and allocations over the same week, so My week can say
  // "on leave" or "at a customer" rather than showing an empty day.
  const mineAbsences = useScheduleAbsences(
    { from: toIsoDate(weekStart), to: toIsoDate(addDays(weekStart, 6)), userEmail: user?.email ?? "" },
    view === "mine" && Boolean(user?.email),
  );

  // When this engineer is next on a rotation. Its own query, deliberately:
  // it looks forward from today rather than at whatever week the reader has
  // navigated to, so the answer does not change as they page around.
  const nextFrom = useMemo(() => new Date(), []);
  const upcoming = useScheduleAssignments(
    {
      from: toIsoDate(nextFrom),
      to: toIsoDate(addDays(nextFrom, NEXT_ROTATION_HORIZON_DAYS)),
      userEmail: user?.email ?? "",
    },
    Boolean(user?.email),
  );

  // Off rota follows the CRE/SRE choice like everything else on the page.
  // Without this it showed every absence in the company, so SRE's column
  // carried CRE's migration allocations -- people SRE has no relationship to.
  // The search filters by team, so an unfiltered view passes the family's own
  // teams rather than nothing.
  const absenceTeamKeys = teamKeys ?? teamsOf(family);
  // The week view reads leave too, for its leave row; `from`/`to` are already
  // the week there.
  const dayAbsences = useScheduleAbsences({ from, to, teamKeys: absenceTeamKeys }, dayView || view === "week");
  const rosterAbsences = useScheduleAbsencesByMonth(
    { teamKeys: absenceTeamKeys },
    rosterMonths,
    rosterView,
  );
  const absences: RotaRead<ScheduleAbsencesResponse> = rosterView ? rosterAbsences : dayAbsences;

  const shifts = useMemo(() => shiftsByCode(catalogue.data?.shifts ?? []), [catalogue.data?.shifts]);

  // The history of the lead's own teams over the months on screen. Read only
  // while the panel is open -- it is a question a lead asks now and then, not
  // one every page load should pay for.
  const activity = useTeamActivity(leadTeams.data ?? [], rosterMonths, showRecent && view === "roster");
  // Memoised rather than written inline: `?? []` builds a fresh array on every
  // render, so every memo downstream that depends on it recomputes every time
  // -- and the React Compiler refuses to optimise a component whose manual
  // memoization it cannot preserve. Flagged as a warning since this page was
  // written; adding another consumer turned it into an error.
  const zones = useMemo(() => catalogue.data?.zones ?? [], [catalogue.data?.zones]);
  const rows = useMemo(
    () => assignments.data?.assignments ?? [],
    [assignments.data?.assignments],
  );

  /** Engineers' names by id, for the history, which keeps only ids. Taken from
   *  what the roster already has loaded -- the same people the changes are to. */
  const namesById = useMemo(() => {
    const m = new Map<string, string>();
    for (const a of rows) m.set(a.engineer.userId, a.engineer.name);
    for (const ab of absences.data?.absences ?? []) m.set(ab.engineer.userId, ab.engineer.name);
    return m;
  }, [rows, absences.data?.absences]);
  const nameOf = (userId: string): string | undefined => namesById.get(userId);

  /** A lead picked a cell on the roster. The roster says which slot and
   *  where on screen; the picker does the rest. */
  const editCell = (edit: {
    userId: string;
    name: string;
    teamKey: string;
    rotaDate: string;
    shiftCode?: string;
    anchor: { top: number; left: number; bottom: number; right: number };
  }): void => {
    setPicker({ ...edit, baseShiftCode: baseShiftFor(edit.userId) });
  };

  /** The standing window this engineer sits in on an ordinary weekday, read
   *  off what they already hold rather than assumed.
   *
   *  CRE runs two of them -- Lanka and India hours -- so which one somebody
   *  goes back to when a rotation is cleared is a fact about that engineer,
   *  not about their group, and guessing it would quietly move people onto
   *  the wrong clock. */
  const baseShiftFor = (userId: string): string | undefined => {
    const seen = new Map<string, number>();
    for (const a of rows) {
      if (a.engineer.userId !== userId) continue;
      if (isRotationShift(shifts.get(a.shiftCode))) continue;
      seen.set(a.shiftCode, (seen.get(a.shiftCode) ?? 0) + 1);
    }
    let best: string | undefined;
    let most = 0;
    for (const [code, n] of seen) {
      if (n > most) [best, most] = [code, n];
    }
    return best;
  };

  /** The windows this group runs, which is what the picker offers. Filtered
   *  by family and nothing narrower: the catalogue's own codes are already
   *  CRE or SRE, so there is no second rule to keep in step with. */
  /** What a lead may mark somebody away for: leave, and only leave.
   *
   *  An allocation -- a customer engagement, an onboarding -- is not the ABT
   *  lead's call to make from a rota grid, so those kinds stay read-only here
   *  even though the grid shows them. Read off the catalogue's own bucket
   *  rather than a list of codes, so a leave kind added later appears without
   *  a change here. */
  /** Who changed which cell, over exactly the months the roster is showing.
   *
   *  Only asked for while the roster is open -- no other view marks a cell --
   *  and kept out of the rota's own query so the grid never waits on it. */
  const editMarkers = useScheduleEditMarkers(rosterMonths, view === "roster");

  /** Keyed for the roster to look up a cell in one step rather than scanning. */
  const editedCells = useMemo(() => {
    const out = new Map<string, { actor: string; changedAt: string }>();
    for (const m of editMarkers.markers) {
      out.set(`${m.userId}|${m.rotaDate}`, { actor: m.actor, changedAt: m.changedAt });
    }
    return out;
  }, [editMarkers.markers]);

  const leaveKinds = useMemo(
    () => (catalogue.data?.absenceKinds ?? []).filter((k) => k.bucket === "LEAVE"),
    [catalogue.data?.absenceKinds],
  );

  /** The windows the picker offers for the cell that is open.
   *
   *  Always the group's own, and on an SRE day split across zone columns also
   *  only that zone's: the column a lead clicked is the zone they mean, and
   *  offering TZ1's windows from the TZ2 column invites a mis-click that is
   *  invisible afterwards -- the cell fills in, just in the wrong lane.
   *
   *  Windows belonging to no zone stay on offer either way. SRE regular hours
   *  is not a zone's to own, and a lead putting somebody back on ordinary
   *  hours should not have to leave the column to do it. */
  const pickerShifts = useMemo(() => {
    const zone = picker?.zoneCode;
    return [...shifts.values()].filter(
      (sh) => sh.family === family && (!zone || !sh.zoneCode || sh.zoneCode === zone),
    );
  }, [shifts, family, picker?.zoneCode]);

  const applyToCell = (shiftCode: string, from: string, to: string): void => {
    if (!picker) return;
    applyRange.mutate(
      {
        userId: picker.userId,
        teamKey: picker.teamKey,
        shiftCode,
        from,
        to,
        note: "set from the month roster",
      },
      {
        onSuccess: () =>
          recordChange(picker, from, to, shifts.get(shiftCode)?.label ?? shiftCode),
        onError: (err) =>
          showError("That change to the rota was not saved. Nothing has moved.", err),
        onSettled: () => setPicker(null),
      },
    );
  };

  const recordChange = (target: CellPickerTarget, from: string, to: string, what: string): void =>
    setChanges((cs) => [...cs, { userId: target.userId, name: target.name, from, to, what }]);

  const markAway = (kindCode: string, from: string, to: string): void => {
    if (!picker) return;
    applyAbsence.mutate(
      {
        userId: picker.userId,
        teamKey: picker.teamKey,
        kindCode,
        from,
        to,
        note: "marked from the month roster",
      },
      {
        onSuccess: () =>
          recordChange(
            picker,
            from,
            to,
            kindCode
              ? (leaveKinds.find((k) => k.code === kindCode)?.label ?? kindCode)
              : "back on the rota",
          ),
        onError: (err) =>
          showError("That change to who is away was not saved. Nothing has moved.", err),
        onSettled: () => setPicker(null),
      },
    );
  };

  /** Clearing a cell means two different writes depending on what is on it.
   *
   *  Somebody marked away comes back by removing the absence, and the rota
   *  underneath -- which was never deleted, only covered -- shows through
   *  again. Rewriting the assignment as well would churn the history with a
   *  change that did not happen. Anything else is a rota slot, so it goes
   *  back to the standing window the way it always did. */
  const clearCell = (shiftCode: string, from: string, to: string): void => {
    if (!picker) return;
    if (picker.absenceKindCode) {
      markAway("", from, to);
      return;
    }
    applyToCell(shiftCode, from, to);
  };

  // SRE works in time zones, so its day is a lane per zone. CRE runs on one
  // clock, so it gets one lane.
  /** The zones SRE actually staffs on the day being viewed.
   *
   *  The catalogue's weekend windows are TZ1 and TZ2 only -- there is no
   *  weekend TZ3 for anyone to be rostered into -- so a weekend earns two
   *  lanes and a weekday three. Read from the shifts rather than written down
   *  here, which is the same rule the month roster follows and from the same
   *  place: a rota change lands in both without a code change, and a lane can
   *  never appear that nobody could be working in.
   *
   *  Falling back to every zone if the catalogue yields none is deliberate --
   *  an empty ladder would read as "nobody is on" rather than as a catalogue
   *  that failed to load. */
  const zonesOnDay = useMemo(() => {
    const weekend = anchor.getDay() === 0 || anchor.getDay() === 6;
    const scope = weekend ? "WEEKEND" : "WEEKDAY";
    const staffed = new Set<string>();
    for (const sh of shifts.values()) {
      if (sh.family !== "SRE" || !sh.zoneCode) continue;
      if (sh.dayScope === scope || sh.dayScope === "ANY") staffed.add(sh.zoneCode);
    }
    const kept = zones.filter((z) => staffed.has(z.code));
    return kept.length > 0 ? kept : zones;
  }, [anchor, shifts, zones]);

  const lanes: LadderLane[] = useMemo(() => {
    if (family === "CRE") {
      return [
        { name: "Rotations", sub: "on-call, the night and regular hours", colour: "var(--muted)", assignments: rows },
      ];
    }
    return zonesOnDay.map((z) => ({
      name: z.code,
      sub: z.label,
      colour: zoneColour(z.code),
      // The same resolution the month roster uses: an assignment's own zone,
      // else its window's, so a zoned window stored without one still lands.
      assignments: rows.filter((a) => (a.zoneCode ?? shifts.get(a.shiftCode)?.zoneCode) === z.code),
      // Escalation on one side, everyone else in the zone on the other.
      layout: "zone" as const,
    }));
  }, [family, rows, shifts, zonesOnDay]);

  if (catalogue.isError) {
    return <QueryErrorState message="Could not load the schedule catalogue." error={catalogue.error} />;
  }

  const busy = catalogue.isLoading || (view === "mine" ? mine.isLoading : assignments.isLoading);

  return (
    <TeamColourProvider teams={catalogue.data?.teams ?? []}>
    <div className="csm-ts" style={SCHEDULE_THEME_VARS}>
      <div className="wrap">
        <h1>Team Schedule</h1>

        <div className="tabrow">
          <div className="tabs" role="tablist">
            {visibleTabs.map((t) => {
              const off = !appliesToView(t);
              return (
              <button
                key={t}
                id={`ts-tab-${t}`}
                className={`tab ${view === t ? "on" : ""}${off ? " off" : ""}`}
                role="tab"
                // The CSS class said which tab was active; nothing did for a
                // screen reader, which read four equal buttons and a panel
                // belonging to none of them.
                aria-selected={view === t}
                aria-controls="ts-panel"
                disabled={off}
                aria-disabled={off}
                title={
                  !off
                    ? undefined
                    : isManager
                      ? "You hold no rota, so this view has nothing of yours to show."
                      : `You are on ${myFamily}. Only “Who is working today” applies to ${family}.`
                }
                onClick={() => setTab(t)}
              >
                <span className="tl">{TITLE[t]}</span>
                <span className="tsub">
                  {t === "today"
                    ? fmtShort(anchor)
                    : t === "roster"
                      ? fmtMonthRange(rosterStart, rosterEnd)
                      : `${fmtShort(weekStart)} – ${fmtShort(addDays(weekStart, 6))}`}
                </span>
              </button>
              );
            })}
          </div>

          <div className="tabright">
            <div className="controls">
              {/* The group and team controls used to live here. Every card now
                  carries them in its own head, where the reader is actually
                  looking, so a second copy up here is just something else to
                  keep in step. The toolbar keeps what is genuinely about the
                  page rather than the card: which date you are on. */}
              <div className="monthnav">
                {/* Two granularities, because a month roster needs both: the
                    double chevron moves by whatever the view is about -- a
                    week, a month -- and the single one always moves a day.
                    Without the day step, picking out the 14th on the roster
                    meant opening the calendar; without the period step,
                    reaching next month meant thirty clicks.

                    On the day view the two would do the same thing, so only
                    one pair is shown. */}
                {dayView ? null : (
                  <button
                    onClick={() => setAnchor(stepBy(anchor, view, -1))}
                    title={rosterView ? "Previous month" : "Previous week"}
                    aria-label={rosterView ? "Previous month" : "Previous week"}
                  >
                    &laquo;
                  </button>
                )}
                <button
                  onClick={() => setAnchor(addDays(anchor, -1))}
                  title="Previous day"
                  aria-label="Previous day"
                >
                  &lsaquo;
                </button>
                <span className="lbl">
                  <b>
                    {dayView
                      ? fmtLong(anchor)
                      : rosterView
                        ? fmtMonthRange(rosterStart, rosterEnd)
                        : `${fmtShort(weekStart)} – ${fmtShort(addDays(weekStart, 6))}`}
                  </b>
                  {/* The day the single chevrons are moving. Without it, a day
                      step inside the same week changes nothing on screen and
                      the button reads as broken. */}
                  {dayView ? null : <i className="on">{fmtShort(anchor)}</i>}
                </span>
                <button
                  onClick={() => setAnchor(addDays(anchor, 1))}
                  title="Next day"
                  aria-label="Next day"
                >
                  &rsaquo;
                </button>
                {dayView ? null : (
                  <button
                    onClick={() => setAnchor(stepBy(anchor, view, 1))}
                    title={rosterView ? "Next month" : "Next week"}
                    aria-label={rosterView ? "Next month" : "Next week"}
                  >
                    &raquo;
                  </button>
                )}
                {/* Stepping a day at a time is fine for next week and hopeless
                    for next quarter, so the date is also directly selectable. */}
                <label className="jump" title="Jump to a date">
                  <svg
                    width="15"
                    height="15"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    aria-hidden="true"
                  >
                    <rect x="3" y="5" width="18" height="16" rx="2" />
                    <path d="M8 3v4M16 3v4M3 10h18" />
                  </svg>
                  <input
                    id="ts-date-jump"
                    type="date"
                    aria-label="Jump to a date"
                    value={toIsoDate(anchor)}
                    onChange={(e) => {
                      const picked = e.target.value;
                      if (!picked) return;
                      // Parsed as local midnight, not UTC: `new Date("2026-09-21")`
                      // is UTC midnight, which lands on the 20th for anyone west
                      // of Greenwich and silently shows the wrong day.
                      const [y, m, d] = picked.split("-").map(Number);
                      setAnchor(new Date(y, m - 1, d));
                    }}
                  />
                </label>
              </div>
              <button
                className="btn"
                onClick={() => {
                  setAnchor(new Date());
                  // Pressing Today when today is already the anchor changes no state,
                  // so a view that scrolled away would sit where it is. The press is
                  // the request, not the date, so it is counted rather than compared.
                  setFocusRequest((n) => n + 1);
                }}
              >
                Today
              </button>
            </div>

            {canEditRota && view === "roster" ? (
              <button
                className={`btn sm${showRecent ? " primary" : ""}`}
                aria-pressed={showRecent}
                onClick={() => setShowRecent((v) => !v)}
                title="What has changed on your teams' rota and leave"
              >
                Recent changes
              </button>
            ) : null}
            {canEditRota ? (
              <button
                className={`btn sm${editing ? " primary" : ""}`}
                aria-pressed={editing}
                onClick={() => {
                  setEditing((v) => !v);
                  setPicker(null);
                  // Done editing is "I am finished": the marks have done their
                  // job, and the next session starts clean.
                  setChanges([]);
                }}
              >
                <svg
                  width="14"
                  height="14"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                  aria-hidden="true"
                >
                  <path d="M4 20h4l10-10a2.8 2.8 0 0 0-4-4L4 16v4z" />
                </svg>
                <span>{editing ? "Done editing" : "Edit rota"}</span>
              </button>
            ) : null}
          </div>
        </div>

        {/* Only on the roster: it is the only view a cell can be clicked in,
            and a bar saying "click a cell" above a view with none to click
            reads as something broken. Names the teams because the grid shows
            every team and only the reader's own rows are live. */}
        {canEditRota && editing && view === "roster" ? (
          <div className="editbar">
            <span className="pill">Editing</span>
            <span>
              {(leadTeams.data ?? [])
                .map((t) => t.charAt(0).toUpperCase() + t.slice(1))
                .join(", ")}
            </span>
            {changes.length === 0 ? (
              <span className="hintx">
                click a cell in your own team&rsquo;s rows to change that day
                &middot; changes save as you make them
              </span>
            ) : (
              // What has changed, so a lead can see their own edits: the count,
              // and the last one spelled out. Every changed cell is ringed on
              // the grid below until Done editing.
              <span className="hintx" role="status">
                <b>
                  {changes.length} change{changes.length === 1 ? "" : "s"}
                </b>{" "}
                &middot; last: {changes[changes.length - 1].name},{" "}
                {fmtRange(changes[changes.length - 1].from, changes[changes.length - 1].to)} &rarr;{" "}
                {changes[changes.length - 1].what}
                <i className="chg-key" aria-hidden="true" /> changed this session
              </span>
            )}
          </div>
        ) : null}

        {canEditRota && showRecent && view === "roster" ? (
          <RecentChanges
            activity={activity.data ?? []}
            isLoading={activity.isLoading}
            isError={activity.isError}
            nameOf={nameOf}
            shiftLabel={(code) => shifts.get(code)?.label ?? code}
            kindLabel={(code) =>
              catalogue.data?.absenceKinds.find((k) => k.code === code)?.label ??
              code.replace(/_/g, " ").toLowerCase()
            }
            onClose={() => setShowRecent(false)}
          />
        ) : null}

        {/* Above the card, not in it: this answers a question about the reader,
            so the answer must not change when they click to another view. Not
            for a manager: they hold no rota, so it could only ever say
            "nothing rostered". */}
        {isManager ? null : (
          <NextRotation
            mine={upcoming.data?.assignments ?? []}
            shifts={shifts}
            tz={tz}
            horizonDays={NEXT_ROTATION_HORIZON_DAYS}
            isLoading={upcoming.isLoading}
          />
        )}

        <div className="card" id="ts-panel" role="tabpanel" aria-labelledby={`ts-tab-${view}`}>
          {assignments.isError ? (
            <QueryErrorState message="Could not load the rota." error={assignments.error} />
          ) : busy ? (
            <div className="offnone">Loading the rota…</div>
          ) : view === "today" ? (
            <DayLadder
              day={anchor}
              tz={tz}
              zoneLabel={zoneAbbreviation(tz)}
              lanes={lanes}
              shifts={shifts}
              zones={zones}
              absences={absences.data?.absences ?? []}
              absenceKinds={catalogue.data?.absenceKinds ?? []}
              {...scopeControls}
            />
          ) : view === "week" ? (
            <WeekTable
              weekStart={weekStart}
              assignments={rows}
              shifts={shifts}
              absences={absences.data?.absences ?? []}
              absenceKinds={catalogue.data?.absenceKinds ?? []}
              {...scopeControls}
            />
          ) : view === "roster" ? (
            <MonthRoster
              selectedIso={toIsoDate(anchor)}
              focusRequest={focusRequest}
              meEmail={user?.email}
              leadTeams={leadTeams.data ?? []}
              editedCells={editedCells}
              editing={editing}
              onEditCell={editCell}
              changedCells={editing ? changedCells : undefined}
              month={rosterStart}
              monthCount={rosterMonths.length}
              assignments={rows}
              absences={absences.data?.absences ?? []}
              shifts={shifts}
              absenceKinds={catalogue.data?.absenceKinds ?? []}
              {...scopeControls}
            />
          ) : (
            <MyWeekStrip
              weekStart={weekStart}
              mine={mine.data?.assignments ?? []}
              everyone={rows}
              shifts={shifts}
              tz={tz}
              myAbsences={mineAbsences.data?.absences ?? []}
              absenceKinds={catalogue.data?.absenceKinds ?? []}
            />
          )}
        </div>
      </div>

      {/* Outside the card on purpose: the card is the page's one scroller, and
          anything positioned inside it is clipped at its edge. */}
      {picker ? (
        <CellPicker
          // Keyed on the cell, so picking another one starts a fresh range
          // rather than inheriting the last cell's end date.
          key={`${picker.userId}|${picker.rotaDate}|${picker.zoneCode ?? ""}`}
          target={picker}
          shifts={pickerShifts}
          busy={applyRange.isPending || applyAbsence.isPending}
          leaveKinds={leaveKinds}
          onApply={applyToCell}
          onMarkAway={markAway}
          onClear={clearCell}
          onClose={() => setPicker(null)}
        />
      ) : null}
    </div>
    </TeamColourProvider>
  );
}

export type { ScheduleAssignment };

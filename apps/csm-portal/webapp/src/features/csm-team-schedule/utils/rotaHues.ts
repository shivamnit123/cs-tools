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

/**
 * The palette teams are coloured from.
 *
 * A list of colours, not a list of teams. The previous version mapped eleven
 * organisation team keys to hexes in committed source, which is the coupling
 * CSM_TEAM_REGISTRY exists to avoid -- raised in review, and correct.
 *
 * A team's colour is now its position in the list the catalogue serves, so
 * every team keeps a distinct colour and no team is named here. Deriving a
 * hue from the key instead was tried and rejected: it collapsed eleven teams
 * onto eight colours, because no hash can promise separation for a key set it
 * has never seen.
 *
 * Ordered so that neighbours in the list are far apart in hue, since teams
 * next to each other in the roster are the ones most often compared.
 */
export const TEAM_PALETTE: readonly string[] = [
  "#4a7fe0",
  "#e8962a",
  "#2f9e8f",
  "#c0559b",
  "#8a63d2",
  "#d95c5c",
  "#3aa889",
  "#c9a227",
  "#5b8ff9",
  "#a0562a",
  // Not grey here: grey is what an unknown team falls back to, and a real
  // team drawing in it is indistinguishable from one the catalogue has never
  // heard of. The eleventh team hit exactly that.
  "#7d8c21",
  "#5a6acf",
];

/**
 * The SRE time zones, in the prototype's own hues.
 *
 * These were JS constants there, not CSS tokens -- which is why an earlier
 * version of this page referenced --tz1-fg and friends, found nothing, and
 * drew all three lanes in the same fallback grey. A zone's colour is how a
 * reader tells the three columns apart at a glance, so it is worth being
 * explicit about.
 */
export const ZONE_COLOURS: Record<string, string> = {
  TZ1: "#e8962a",
  TZ2: "#4a7fe0",
  TZ3: "#8a63d2",
};

/** The colour for a time zone, falling back to a neutral for one not listed. */
export function zoneColour(code: string): string {
  return ZONE_COLOURS[code.toUpperCase()] ?? "#6b7280";
}

/**
 * The colour for a team, by its position in the catalogue.
 *
 * `order` comes from the served team list. A team the caller has no position
 * for -- one that has left the registry but still has rota history -- falls
 * back to a neutral rather than borrowing somebody else's colour.
 */
export function teamColour(order: number | undefined): string {
  if (order === undefined || order < 0) return "#6b7280";
  return TEAM_PALETTE[order % TEAM_PALETTE.length];
}

/**
 * Chips drawn as light text on a dark fill. For these the text colour is
 * white, so a card or row tinted "in the chip's colour" by its text colour was
 * tinted white -- the evening card's title all but vanished on a light page.
 * Their fill is the colour that identifies them.
 */
const DARK_CHIPS = new Set(["pm", "ext", "al", "ll", "onb", "exc"]);

/** The colour a card, cell or row takes from a chip's colour token: the
 *  chip's text colour, or its fill for a chip that is dark with light text. */
export function accentOf(token: string | undefined, fallback = "var(--faint)"): string {
  const t = (token ?? "").toLowerCase();
  if (!t) return fallback;
  return DARK_CHIPS.has(t) ? `var(--${t}-bg, ${fallback})` : `var(--${t}-fg, ${fallback})`;
}

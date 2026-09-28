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


import { describe, expect, it } from "vitest";
import { initialsOf } from "./rota";
import { TEAM_PALETTE, accentOf, teamColour } from "./rotaHues";

describe("initialsOf", () => {
  it("takes the first and last word", () => {
    expect(initialsOf("Jane Doe")).toBe("JD");
  });
  it("skips a numbering suffix rather than using its digit", () => {
    expect(initialsOf("Acme Engineer 11")).toBe("AE");
  });
  it("uses two letters of a single name", () => {
    expect(initialsOf("Jane")).toBe("JA");
  });
});

describe("accentOf", () => {
  it("takes a light chip's text colour", () => {
    expect(accentOf("LK")).toBe("var(--lk-fg, var(--faint))");
  });
  it("takes a dark chip's fill, since its text is white", () => {
    expect(accentOf("PM")).toBe("var(--pm-bg, var(--faint))");
  });
  it("falls back when there is no token", () => {
    expect(accentOf(undefined)).toBe("var(--faint)");
  });
});

describe("teamColour", () => {
  it("gives every position in the palette its own colour", () => {
    const seen = TEAM_PALETTE.map((_, i) => teamColour(i));
    expect(new Set(seen).size).toBe(TEAM_PALETTE.length);
  });

  it("never gives a real team the colour an unknown one falls back to", () => {
    // The eleventh team landed on a grey identical to the fallback, so a real
    // team was indistinguishable from one the catalogue had never heard of.
    const unknown = teamColour(undefined);
    for (let i = 0; i < TEAM_PALETTE.length; i += 1) {
      expect(teamColour(i)).not.toBe(unknown);
    }
  });

  it("falls back for a team with no position", () => {
    expect(teamColour(undefined)).toBe(teamColour(-1));
  });
});

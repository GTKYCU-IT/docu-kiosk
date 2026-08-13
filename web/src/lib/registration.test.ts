import { describe, expect, it } from "vitest";
import {
  classifyRegistration,
  PROBLEM_ALREADY_REGISTERED,
  PROBLEM_NAME_CONFLICT,
} from "./registration";

// Build an RFC 9457 problem document with the discriminator `type` plus
// arbitrary wording fields, mirroring the server's responses.
const problem = (type: string, wording: Record<string, unknown> = {}) =>
  JSON.stringify({ type, title: "problem", detail: "see type", ...wording });

describe("classifyRegistration", () => {
  it("classifies 204 as a registered new identity regardless of body and content type", () => {
    expect(classifyRegistration(204, null, "")).toEqual({ kind: "registered" });
    expect(classifyRegistration(204, "text/plain", "{not json")).toEqual({
      kind: "registered",
    });
    expect(classifyRegistration(204, "application/problem+json", "{}")).toEqual({
      kind: "registered",
    });
  });

  it("classifies the kiosk-already-registered problem type", () => {
    expect(
      classifyRegistration(
        409,
        "application/problem+json",
        problem(PROBLEM_ALREADY_REGISTERED),
      ),
    ).toEqual({ kind: "already-registered" });
  });

  it("classifies the kiosk-name-conflict problem type", () => {
    expect(
      classifyRegistration(409, "application/problem+json", problem(PROBLEM_NAME_CONFLICT)),
    ).toEqual({ kind: "name-conflict" });
  });

  it.each<[string, number, string | null, string]>([
    ["a missing content type", 409, null, problem(PROBLEM_ALREADY_REGISTERED)],
    ["an empty content type", 409, "", problem(PROBLEM_ALREADY_REGISTERED)],
    ["a text/plain content type", 409, "text/plain", problem(PROBLEM_ALREADY_REGISTERED)],
    ["an application/json content type", 409, "application/json", problem(PROBLEM_ALREADY_REGISTERED)],
  ])("rejects a non-problem response with %s even with a problem-shaped body", (_label, status, contentType, body) => {
    expect(classifyRegistration(status, contentType, body)).toEqual({ kind: "rejected" });
  });

  it.each<[string, string]>([
    ["malformed JSON", "{not json"],
    ["a string body", '"hello"'],
    ["a number body", "42"],
    ["a null body", "null"],
    ["an array body", '[{"type":"' + PROBLEM_ALREADY_REGISTERED + '"}]'],
    ["an empty object", "{}"],
    ["a non-string type", '{"type":42}'],
  ])("rejects a problem document with %s", (_label, body) => {
    expect(classifyRegistration(409, "application/problem+json", body)).toEqual({
      kind: "rejected",
    });
  });

  it.each<[string, string]>([
    ["invalid-kiosk-name", "urn:docu-kiosk:problem:invalid-kiosk-name"],
    ["internal-error", "urn:docu-kiosk:problem:internal-error"],
    ["malformed-request", "urn:docu-kiosk:problem:malformed-request"],
    ["an unrelated urn", "urn:example:other"],
  ])("rejects an unrecognized problem type: %s", (_label, type) => {
    expect(classifyRegistration(422, "application/problem+json", problem(type))).toEqual({
      kind: "rejected",
    });
  });

  it("accepts the problem content type case-insensitively and with parameters", () => {
    expect(
      classifyRegistration(
        409,
        "Application/Problem+JSON; charset=utf-8",
        problem(PROBLEM_ALREADY_REGISTERED),
      ),
    ).toEqual({ kind: "already-registered" });
    expect(
      classifyRegistration(409, " application/problem+json ", problem(PROBLEM_NAME_CONFLICT)),
    ).toEqual({ kind: "name-conflict" });
  });

  it("classifies solely by problem type, independent of title/detail wording", () => {
    const misleadingWording = {
      title: "Kiosk name is already taken",
      detail: "pick a different name",
    };
    expect(
      classifyRegistration(
        409,
        "application/problem+json",
        problem(PROBLEM_ALREADY_REGISTERED, misleadingWording),
      ),
    ).toEqual({ kind: "already-registered" });

    const oppositeWording = {
      title: "Welcome back, kiosk already registered",
      detail: "your identity exists",
    };
    expect(
      classifyRegistration(409, "application/problem+json", problem(PROBLEM_NAME_CONFLICT, oppositeWording)),
    ).toEqual({ kind: "name-conflict" });
  });
});

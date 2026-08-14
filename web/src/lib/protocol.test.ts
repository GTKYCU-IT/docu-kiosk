import { describe, expect, it } from "vitest";
import { encodeStatus, parse } from "./protocol";

describe("encodeStatus", () => {
  it("emits the exact ready frame bytes", () => {
    expect(encodeStatus("ready")).toBe('{"type":"status","status":"ready"}');
  });

  it("emits the exact signing frame bytes", () => {
    expect(encodeStatus("signing")).toBe('{"type":"status","status":"signing"}');
  });
});

describe("parse", () => {
  it("decodes a connected message", () => {
    expect(parse('{"type":"connected","name":"lobby-1"}')).toEqual({
      type: "connected",
      name: "lobby-1",
    });
  });

  it("decodes a sign message", () => {
    expect(parse('{"type":"sign","url":"https://sign.example/abc"}')).toEqual({
      type: "sign",
      url: "https://sign.example/abc",
    });
  });

  it("ignores extra fields beyond the known shape", () => {
    expect(
      parse('{"type":"connected","name":"lobby-1","extra":42,"nested":{"a":1}}'),
    ).toEqual({ type: "connected", name: "lobby-1" });
    expect(parse('{"type":"sign","url":"https://sign.example/abc","ts":123}')).toEqual({
      type: "sign",
      url: "https://sign.example/abc",
    });
  });

  it.each([
    ["an empty string", ""],
    ["unparseable text", "{not json"],
    ["truncated JSON", '{"type":"connected"'],
  ])("rejects %s as invalid JSON", (_label, raw) => {
    expect(() => parse(raw)).toThrow(Error);
  });

  it.each([
    ["a number", "42"],
    ["a string", '"hello"'],
    ["null", "null"],
    ["true", "true"],
    ["an array", '[{"type":"connected","name":"lobby-1"}]'],
  ])("rejects a non-object payload: %s", (_label, raw) => {
    expect(() => parse(raw)).toThrow(Error);
  });

  it.each([
    ["a missing type", '{"name":"lobby-1"}'],
    ["an unknown type", '{"type":"ping"}'],
    ["a non-string type", '{"type":42}'],
  ])("rejects a message with %s", (_label, raw) => {
    expect(() => parse(raw)).toThrow(Error);
  });

  it.each([
    ["a missing name", '{"type":"connected"}'],
    ["a non-string name", '{"type":"connected","name":5}'],
    ["a null name", '{"type":"connected","name":null}'],
  ])("rejects a connected message with %s", (_label, raw) => {
    expect(() => parse(raw)).toThrow(Error);
  });

  it.each([
    ["a missing url", '{"type":"sign"}'],
    ["a non-string url", '{"type":"sign","url":5}'],
    ["a null url", '{"type":"sign","url":null}'],
  ])("rejects a sign message with %s", (_label, raw) => {
    expect(() => parse(raw)).toThrow(Error);
  });
});

import { dayLevel, ms, percent } from "./format";

describe("format", () => {
  it("never rounds a failing day up to 100 %", () => {
    expect(percent(99.999)).toBe("99.99 %");
    expect(percent(null)).toBe("—");
  });

  it("switches to seconds above one second", () => {
    expect(ms(954.6)).toBe("955 ms");
    expect(ms(2500)).toBe("2.50 s");
  });

  it("buckets daily uptime", () => {
    expect(dayLevel(100)).toBe("ok");
    expect(dayLevel(99)).toBe("slow");
    expect(dayLevel(95)).toBe("degraded");
    expect(dayLevel(50)).toBe("down");
    expect(dayLevel(null)).toBe("none");
  });
});

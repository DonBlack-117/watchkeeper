import "@testing-library/jest-dom/vitest";

// jsdom has no ResizeObserver; the chart keeps its default width in tests.
if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

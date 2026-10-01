import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

import { describe, expect, it } from "vitest";

const currentDir = dirname(fileURLToPath(import.meta.url));
const groupsViewSource = readFileSync(
  resolve(currentDir, "../GroupsView.vue"),
  "utf8",
);

describe("groups model allowlist layout", () => {
  it("keeps the toolbar outside of the scrolling list content", () => {
    for (const mode of ["create", "edit"]) {
      const panelStart = groupsViewSource.indexOf(
        `v-if="${mode}ModelAllowlistState.enabled"`,
      );
      const itemsStart = groupsViewSource.indexOf(
        `v-for="(item, index) in ${mode}ModelAllowlistState.items"`,
        panelStart,
      );
      expect(panelStart).toBeGreaterThanOrEqual(0);
      expect(itemsStart).toBeGreaterThan(panelStart);
      const allowlistPanel = groupsViewSource.slice(panelStart, itemsStart);
      expect(allowlistPanel).toContain("overflow-hidden rounded-lg border");
      expect(allowlistPanel).toContain("max-h-64 space-y-2 overflow-y-auto p-2");
      expect(allowlistPanel).not.toContain("sticky top-0");
    }
  });

  it("uses a wide dialog and keeps model pricing controls responsive", () => {
    expect(groupsViewSource).toContain('width="wide"');
    expect(groupsViewSource).toContain(
      "btn btn-secondary shrink-0 whitespace-nowrap",
    );
  });
});

import test from "node:test";
import assert from "node:assert/strict";
import { pageNumbers } from "./pageNumbers.ts";

test("small and empty totals remain bounded", () => {
  assert.deepEqual(pageNumbers(1, 0), [1]);
  assert.deepEqual(pageNumbers(1, 1), [1]);
  assert.deepEqual(pageNumbers(3, 5), [1, 2, 3, 4, 5]);
  assert.deepEqual(pageNumbers(3, 6), [1, 2, 3, 4, 5, 6]);
});
test("leading, middle and trailing pages expose usable neighborhoods", () => {
  assert.deepEqual(pageNumbers(1, 10), [1, 2, 3, 4, 5, "ellipsis-end", 10]);
  assert.deepEqual(pageNumbers(5, 10), [
    1,
    "ellipsis-start",
    4,
    5,
    6,
    "ellipsis-end",
    10,
  ]);
  assert.deepEqual(pageNumbers(10, 10), [1, "ellipsis-start", 6, 7, 8, 9, 10]);
});
test("invalid input is normalized and enormous totals stay constant-sized", () => {
  assert.deepEqual(pageNumbers(-1, 10), pageNumbers(1, 10));
  assert.deepEqual(pageNumbers(99, 10), pageNumbers(10, 10));
  assert.deepEqual(pageNumbers(NaN, Infinity), [1]);
  assert.equal(pageNumbers(100000, Number.MAX_SAFE_INTEGER).length, 7);
});
test("every current page remains visible, ordered, with first and last pages", () => {
  for (let total = 1; total <= 100; total++) {
    for (let page = 1; page <= total; page++) {
      const visible = pageNumbers(page, total).filter(
        (value) => typeof value === "number",
      );
      assert.equal(visible[0], 1);
      assert.equal(visible.at(-1), total);
      assert.ok(visible.includes(page));
      assert.equal(new Set(visible).size, visible.length);
      assert.deepEqual(
        visible,
        [...visible].sort((a, b) => a - b),
      );
    }
  }
});

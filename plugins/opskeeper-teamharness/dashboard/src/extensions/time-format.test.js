import assert from 'node:assert/strict';
import test from 'node:test';

import {
  formatBeijingClock,
  formatBeijingTime,
  formatBeijingTimeWithUtc,
} from './time-format.js';

test('formats demo timestamps as explicit Beijing time', () => {
  const value = '2026-09-18T17:26:01Z';

  assert.equal(formatBeijingClock(value), '01:26:01 北京时间');
  assert.equal(formatBeijingTime(value), '2026-09-19 01:26:01 北京时间');
});

test('keeps UTC available for cross-timezone evidence review', () => {
  assert.equal(
    formatBeijingTimeWithUtc('2026-09-18T17:26:01Z'),
    '2026-09-19 01:26:01 北京时间 / 2026-09-18 17:26:01 UTC',
  );
});

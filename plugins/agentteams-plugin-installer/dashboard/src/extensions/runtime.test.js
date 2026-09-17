import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

test('uses the foreground token for muted plugin text', () => {
  const extensionsDir = fileURLToPath(new URL('./', import.meta.url));
  const mutedTextPattern = /color:\s*['`]var\(--muted\)['`]/u;
  const violations = [];

  for (const entry of readdirSync(extensionsDir, { withFileTypes: true })) {
    if (!entry.isFile() || !/\.jsx?$/u.test(entry.name)) continue;
    const source = readFileSync(path.join(extensionsDir, entry.name), 'utf8');
    if (mutedTextPattern.test(source)) violations.push(entry.name);
  }

  assert.deepEqual(violations, []);
});

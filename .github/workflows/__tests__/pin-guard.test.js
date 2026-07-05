// Unit test for the PR Failure Report companion's pure log helpers
// (parsePinGuard and extractFailRegion) that live inline in the
// drift-comment.yaml github-script step.
//
// The companion only runs from the base repo on a workflow_run completion, so
// its behavior cannot be exercised from a branch PR. This test proves the
// helpers locally instead: it extracts the exact shipped source between the
// `pure-helpers-start`/`pure-helpers-end` sentinels (so there is no forked
// copy to drift), then runs it against a REAL failing "Unit Tests" job log
// captured from the action-pins guard trip run (PR #452, run 28725833923).
//
// Regression it guards: the failure region (the guard signature and its
// want/got rows) prints in the MIDDLE of the job log; the last lines are
// post-job git-config cleanup ending at "Cleaning up orphan processes". The
// prior extraction tailed the last 30 lines, so parsePinGuard never saw the
// signature and the manifest remediation was silently dropped.
//
// Run: node --test .github/workflows/__tests__/pin-guard.test.js

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const workflowPath = path.join(__dirname, '..', 'drift-comment.yaml');
const fixturePath = path.join(__dirname, 'fixtures', 'unit-tests-pin-guard-fail.log');

// Extract the pure-helper source verbatim from the workflow and evaluate it in
// an isolated context, returning { parsePinGuard, extractFailRegion }.
function loadHelpers() {
  const yaml = fs.readFileSync(workflowPath, 'utf8');
  const start = yaml.indexOf('pure-helpers-start');
  const end = yaml.indexOf('pure-helpers-end');
  assert.ok(start !== -1 && end !== -1 && end > start,
    'pure-helper sentinels must bracket the shipped source');
  // Slice the lines strictly between the two sentinel lines and strip the
  // uniform YAML block indentation so the JS parses standalone.
  const block = yaml.slice(start, end).split('\n').slice(1, -1);
  const dedented = block
    .map((l) => l.replace(/^\s{12}/, ''))
    .join('\n');
  const src = `${dedented}\n;({ parsePinGuard, extractFailRegion });`;
  return vm.runInNewContext(src, {});
}

const { parsePinGuard, extractFailRegion } = loadHelpers();
const log = fs.readFileSync(fixturePath, 'utf8');

// The manifest's canonical checkout SHA and the corrupted "got" SHA the trip
// introduced. parsePinGuard adopts the governed "got" value.
const GOT_SHA = '9c091bb21b7c1c1d1991bb908d89e4e9dddfeabc';

test('parsePinGuard extracts the manifest remediation from the full log', () => {
  const edits = parsePinGuard(log);
  assert.ok(edits, 'guard signature + rows must be found in the full log');
  assert.ok(edits.has('actions/checkout'), 'must name the divergent action');
  const entry = edits.get('actions/checkout');
  assert.equal(entry.sha, GOT_SHA, 'adopts the governed got SHA');
  // The clean governed row wins over the bracketed anchor row, so no stray
  // punctuation leaks into the emitted remediation.
  assert.equal(entry.version, 'v7.0.0');
});

test('extractFailRegion lands on the failing test output, not the cleanup tail', () => {
  const region = extractFailRegion(log);
  assert.match(region, /--- FAIL/, 'region must include the FAIL marker');
  assert.match(region, /want \S+ # \S+ got \S+/, 'region must include a want/got row');
  assert.doesNotMatch(region, /Cleaning up orphan processes/,
    'region must not be the post-job cleanup tail');
  // The display region alone is now rich enough to yield the remediation.
  assert.ok(parsePinGuard(region), 'the failure region must itself parse');
});

test('regression: the old last-30-lines tail missed the guard entirely', () => {
  const oldTail = log
    .split('\n')
    .filter((l) => l.trim().length > 0)
    .slice(-30)
    .join('\n');
  assert.match(oldTail, /Cleaning up orphan processes/,
    'the trailing region is git-config cleanup, as observed live');
  assert.equal(parsePinGuard(oldTail), null,
    'the prior extraction produced no remediation - the bug under fix');
});

test('extractFailRegion falls back to the trailing lines without a signature', () => {
  const plain = 'line one\nline two\nline three';
  assert.equal(extractFailRegion(plain), plain);
});

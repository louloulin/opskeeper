// Legacy-container import — talks to POST /v1/marketplace/import.
//
// This is not install, and the difference is the whole point of the route.
// Install means "this package is admitted and usable now". Import means
// "here is what this package would have to say about itself before anyone
// could admit it". A legacy container — the `.claude-plugin/plugin.json`
// form, the `openclaw.plugin.json` form, a bare `skills.sh` drop — carries
// no statement of what its tools do, no safety level, no scopes and no
// blast radius, so the converter refuses to invent them: the package it
// writes declares the narrowest thing that loads, and the response carries
// the list of questions a human still has to answer.
//
// That is why ImportReport.Decisions is the field this client exists to
// fetch. A converter that filled those in would be claiming to have
// reviewed code it only read the names of, and the console rendering a
// converted package as "ready" would be repeating that claim to an
// operator who is about to put it on hosts.
import { request } from '@/api/client';

/** ImportDecision is one unanswered question about the converted package.
 *  `field` is the manifest path to edit; `question` is what to answer;
 *  `why` is why the answer cannot be derived from the container — which is
 *  what makes it a decision rather than a missing value. All three are
 *  rendered, because `why` is the one that tells a reviewer whether the
 *  converter or the vendor is the one who has to think. */
export interface ImportDecision {
  field: string;
  question: string;
  why: string;
}

/** LoadWarning is a non-fatal finding from the loader that recognised the
 *  container. It is carried through so an import cannot quietly drop a
 *  parse failure and look like a clean conversion. */
export interface LoadWarning {
  path: string;
  reason: string;
  code: string;
}

/** ImportReport is what a conversion produced.
 *
 *  The counts are counts on purpose. The route does not return the paths
 *  of forty extension files, because nobody reviews a list of forty
 *  identical paths and a report that contained one would be skimmed past —
 *  the review surface here is the questions, not the inventory. */
export interface ImportReport {
  /** The container form that was recognised. */
  kind: string;
  name: string;
  version?: string;
  description?: string;
  /** Package-relative paths written for the two resource classes an
   *  operator actually reads: what the agent knows and who it can ask. */
  skills: string[];
  agents: string[];
  prompts: number;
  mcp: number;
  extensions: number;
  decisions: ImportDecision[];
  warnings: LoadWarning[];
}

export interface ImportResponse extends ImportReport {
  /** Where the converted package was written. It is a directory on the
   *  manager's disk, NOT an installed package and NOT on any node: the
   *  route deliberately stops short of publishing so the review the
   *  conversion exists to force cannot be skipped. */
  dest: string;
}

/** importPack uploads a legacy container and returns what it would take to
 *  admit it. Admin-only.
 *
 *  The 409 answer matters and is not an edge case: the route refuses to
 *  write over an existing directory, because a second import of the same
 *  pack would otherwise replace a package an operator had already answered
 *  some of those questions on. A console that retried on 409 would
 *  destroy review work. */
export function importPack(file: File): Promise<ImportResponse> {
  const fd = new FormData();
  fd.append('file', file);
  return request<ImportResponse>('POST', '/marketplace/import', fd);
}

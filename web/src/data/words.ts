// The page's words for pull and merge requests, in one place. A GitHub pull
// request and a GitLab merge request are the same thing on the board; the
// words follow the forge of the one they name, and the general words (column
// hints, the Teams column) name both only once the board has a GitLab merge
// request (Snapshot.gitlabHosts not empty). With GitHub only, every word is
// the one the page used before GitLab.
//
// The general words ("pull and merge requests", "pull or merge request",
// "approval needed") are the wording chosen for GitLab;
// each lives here once so a different choice is a one-line change.
import type { Forge, PullRequest } from '../contract.gen';

/** True when the board shows GitLab merge requests (Index.gitlabHosts). */
export function hasGitLab(gitlabHosts: readonly string[]): boolean {
  return gitlabHosts.length > 0;
}

/** "pull request" or "merge request", for one of them. */
export function requestWord(forge: Forge): string {
  return forge === 'gitlab' ? 'merge request' : 'pull request';
}

/** "GitHub" or "GitLab": where one of them lives. */
export function forgeName(forge: Forge): string {
  return forge === 'gitlab' ? 'GitLab' : 'GitHub';
}

/** One of them, either forge: "pull request", or "pull or merge request" once GitLab is on the board. */
export function anyRequest(gitlab: boolean): string {
  return gitlab ? 'pull or merge request' : 'pull request';
}

/** Several of them, either forge, as a heading: the Teams column, a card's list. */
export function requestsHeading(gitlab: boolean): string {
  return gitlab ? 'Pull and merge requests' : 'Pull requests';
}

/** The hidden words after a linked number: " (merge request, opens in a new tab)". */
export function requestLinkHint(forge: Forge): string {
  return ` (${requestWord(forge)}, opens in a new tab)`;
}

/**
 * The review words on a card's line, "" for none. GitHub keeps its words
 * ("changes requested", "approved"; a review still required says nothing).
 * GitLab has approvals only: "approved", or "approval needed" where its
 * rules ask for one; it never requests changes, so that says nothing.
 */
export function reviewWords(pr: Pick<PullRequest, 'forge' | 'review'>): string {
  if (pr.review === 'approved') return 'approved';
  if (pr.forge === 'gitlab') return pr.review === 'review_required' ? 'approval needed' : '';
  return pr.review === 'changes_requested' ? 'changes requested' : '';
}

/** The catch-up's word for an opened one: "PR opened" or "MR opened". */
export function openedWord(forge: Forge): string {
  return forge === 'gitlab' ? 'MR opened' : 'PR opened';
}

/**
 * About's privacy line. Without GitLab it is the line the page always had.
 * Once the server reads GitLab it names only the forges it reads: GitLab
 * alone, or both when GitHub is read too.
 */
export function privacyLine(reads: { github: boolean; gitlab: boolean }): string {
  if (!reads.gitlab) return 'Runs only on this machine. Nothing is sent anywhere; GitHub is read through your own gh.';
  return reads.github
    ? 'Runs only on this machine. Nothing is sent anywhere; GitHub and GitLab are read through your own gh and glab.'
    : 'Runs only on this machine. Nothing is sent anywhere; GitLab is read through your own glab.';
}

/** A forge's source is read: switched on and found (answering or failing). */
export function sourceRead(sources: readonly { kind: string; state: string }[], kind: 'github' | 'gitlab'): boolean {
  return sources.some((s) => s.kind === kind && (s.state === 'ok' || s.state === 'failing'));
}

/** The In review column's hint. */
export function inReviewHint(gitlab: boolean): string {
  return `Stopped with an open ${anyRequest(gitlab)}.`;
}

/** The In review column's words when it is empty. */
export function inReviewEmpty(gitlab: boolean): string {
  return `Empty right now. An agent lands here when it stops while one of its ${anyRequest(gitlab)}s is still open.`;
}

/** The Done column's hint. */
export function doneHint(gitlab: boolean): string {
  return `Every ${anyRequest(gitlab)} it opened is merged or closed.`;
}

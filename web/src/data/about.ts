// The About panel's words. Build facts come from the snapshot and are
// cleaned; links and the update command are constants, never data.
import type { Build } from '../contract.gen';
import { clean } from './text';

export const UPDATE_COMMAND = 'brew upgrade sitboard';

export interface AboutLink {
  label: string;
  text: string;
  href: string;
}

const REPO = 'https://github.com/IZZY-Agency/sitboard';

export const LINKS: readonly AboutLink[] = [
  { label: 'Website', text: 'sitboard.com', href: 'https://sitboard.com' },
  { label: 'Source', text: 'github.com/IZZY-Agency/sitboard', href: REPO },
  { label: 'Licence', text: 'MIT', href: `${REPO}/blob/main/LICENSE` },
];

export const MORE_LINKS: readonly Omit<AboutLink, 'label'>[] = [
  { text: 'Open-source licences', href: '/licenses.txt' },
  { text: 'An IZZY Agency tool', href: 'https://izzy.agency' },
];

// Only an exact release number counts: "git describe" between tags
// ("0.1.0-3-gabc1234", "0.1.0-dirty") is a development build.
// Each number is at most six digits: the status bar button cannot wrap.
const RELEASE = /^v?(\d{1,6}\.\d{1,6}\.\d{1,6})$/;

/** The release number, or null for a development build. */
export function releaseVersion(version: string): string | null {
  return RELEASE.exec(clean(version))?.[1] ?? null;
}

/** The version beside the name in the status bar: "0.1.0", "dev", or null before the first snapshot. */
export function statusVersion(build: Build | null | undefined): string | null {
  if (!build) return null;
  return releaseVersion(build.version) ?? 'dev';
}

/** "sitboard 0.1.0", "sitboard dev", or "sitboard" before the first snapshot. */
export function statusLabel(build: Build | null | undefined): string {
  const v = statusVersion(build);
  return v ? `sitboard ${v}` : 'sitboard';
}

export function versionText(build: Build): string {
  return releaseVersion(build.version) ?? 'development build';
}

export function commitText(commit: string): string {
  const c = clean(commit);
  if (!c || c === 'unknown') return 'unknown';
  return c.slice(0, 12);
}

const OS: Record<string, string> = { darwin: 'macOS', linux: 'Linux' };
const ARCH: Record<string, string> = { amd64: 'x86_64', arm64: 'arm64' };

export function platformText(os: string, arch: string): string {
  const o = clean(os);
  const a = clean(arch);
  // Own keys only: "constructor" or "__proto__" are text, not lookups into the object.
  const text = [Object.hasOwn(OS, o) ? OS[o] : o, Object.hasOwn(ARCH, a) ? ARCH[a] : a].filter(Boolean).join(' ');
  return text || 'unknown';
}

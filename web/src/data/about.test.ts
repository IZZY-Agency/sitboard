import { describe, expect, it } from 'vitest';
import { LINKS, MORE_LINKS, UPDATE_COMMAND, commitText, platformText, releaseVersion, statusLabel, versionText } from './about';

const build = (version: string, over: Partial<{ commit: string; os: string; arch: string }> = {}) => ({
  version,
  commit: 'abc1234',
  os: 'darwin',
  arch: 'arm64',
  ...over,
});

describe('about', () => {
  it('the status bar label: the release number, "dev" otherwise, the bare name before the first snapshot', () => {
    expect(statusLabel(null)).toBe('sitboard');
    expect(statusLabel(undefined)).toBe('sitboard');
    expect(statusLabel(build('0.1.0'))).toBe('sitboard 0.1.0');
    expect(statusLabel(build('v1.12.3'))).toBe('sitboard 1.12.3');
    expect(statusLabel(build('abc1234'))).toBe('sitboard dev');
    expect(statusLabel(build('dev'))).toBe('sitboard dev');
    expect(statusLabel(build(''))).toBe('sitboard dev');
    // git describe between tags is a development build, not the release it follows.
    expect(statusLabel(build('0.1.0-3-gabc1234'))).toBe('sitboard dev');
    expect(statusLabel(build('0.1.0-dirty'))).toBe('sitboard dev');
    // Each number is at most six digits, so the button never holds a long run of digits.
    expect(statusLabel(build('1234567.0.0'))).toBe('sitboard dev');
    expect(statusLabel(build('123456.123456.123456'))).toBe('sitboard 123456.123456.123456');
  });

  it('the version row: the number for a release, "development build" otherwise', () => {
    expect(versionText(build('0.1.0'))).toBe('0.1.0');
    expect(versionText(build('abc1234-dirty'))).toBe('development build');
    expect(releaseVersion('0.1.0')).toBe('0.1.0');
    expect(releaseVersion('nope')).toBeNull();
  });

  it('the platform: macOS and Linux by name, amd64 as x86_64, anything else as given', () => {
    expect(platformText('darwin', 'arm64')).toBe('macOS arm64');
    expect(platformText('darwin', 'amd64')).toBe('macOS x86_64');
    expect(platformText('linux', 'amd64')).toBe('Linux x86_64');
    expect(platformText('linux', 'arm64')).toBe('Linux arm64');
    expect(platformText('freebsd', 'riscv64')).toBe('freebsd riscv64');
    expect(platformText('', '')).toBe('unknown');
    // Only the table's own names map; inherited object keys are text like any other.
    expect(platformText('constructor', 'toString')).toBe('constructor toString');
    expect(platformText('__proto__', 'hasOwnProperty')).toBe('__proto__ hasOwnProperty');
  });

  it('the commit is short, and "unknown" when absent', () => {
    expect(commitText('abc1234')).toBe('abc1234');
    expect(commitText('0123456789abcdef0123456789abcdef01234567')).toBe('0123456789ab');
    expect(commitText('')).toBe('unknown');
    expect(commitText('unknown')).toBe('unknown');
  });

  it('text from the snapshot is cleaned', () => {
    const rlo = String.fromCodePoint(0x202e);
    expect(platformText(`dar${rlo}win`, 'arm64')).toBe('macOS arm64');
    expect(commitText(`ab${rlo}c1234`)).toBe('abc1234');
    expect(statusLabel(build(`0.1.0${rlo}`))).toBe('sitboard 0.1.0');
  });

  it('the links and the update command are constants', () => {
    expect(UPDATE_COMMAND).toBe('brew upgrade sitboard');
    expect(LINKS.map((l) => [l.label, l.text, l.href])).toEqual([
      ['Website', 'sitboard.com', 'https://sitboard.com'],
      ['Source', 'github.com/IZZY-Agency/sitboard', 'https://github.com/IZZY-Agency/sitboard'],
      ['Licence', 'MIT', 'https://github.com/IZZY-Agency/sitboard/blob/main/LICENSE'],
    ]);
    // The third-party licences are served by the board itself, same origin.
    expect(MORE_LINKS.map((l) => [l.text, l.href])).toEqual([
      ['Open-source licences', '/licenses.txt'],
      ['An IZZY Agency tool', 'https://izzy.agency'],
    ]);
  });
});

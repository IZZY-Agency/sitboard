/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The build is embedded in the Go binary from internal/server/dist. Only
// index.html is an entry: the dev harness in dev/ never reaches the build
// (scripts/check-dist.mjs fails the build if it does).
// `npm run dev` serves on 127.0.0.1, forwards /api to a running sitboard, and
// serves the harness at /dev/ with placeholder data.
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/server/dist',
    emptyOutDir: true,
    // One chunk and no modulepreload links: the polyfill would ship Vite's
    // own code with no use and no licence entry.
    modulePreload: { polyfill: false },
  },
  server: {
    host: '127.0.0.1',
    proxy: { '/api': 'http://127.0.0.1:4317' },
    // The harness reads the contract's golden snapshot.
    fs: { allow: ['.', '../internal/contract/golden'] },
  },
  // Tests mount components in jsdom: use the browser entry points of packages.
  resolve: process.env.VITEST ? { conditions: ['browser'] } : undefined,
  test: {
    include: ['src/**/*.test.ts', 'scripts/**/*.test.ts'],
    environment: 'jsdom',
    css: true,
    setupFiles: ['src/test/setup.ts'],
  },
});

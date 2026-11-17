// Build the web page as `npm run build` does, into <out>/bundle, and record in
// <out>/modules.json what went into it: every module with code in an output
// chunk, every stylesheet in the module graph (its code goes into the CSS
// file, not a chunk), and every emitted asset with the source files it came
// from. Modules the build loaded but dropped entirely (tree-shaken) are not
// shipped and are not listed. The notices
// generator (internal/tools/notices) reads that list: anything bundled is
// either the page's own source or an npm package with a licence entry.
//
//   node scripts/notices-bundle.mjs <web folder> <out folder>
import { existsSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const [webArg, outArg] = process.argv.slice(2);
if (!webArg || !outArg) {
  console.error('usage: node scripts/notices-bundle.mjs <web folder> <out folder>');
  process.exit(2);
}
const web = path.resolve(webArg);
const out = path.resolve(outArg);
mkdirSync(out, { recursive: true });

// Vite from the page's own node_modules, by its package entry.
const vitePkg = path.join(web, 'node_modules', 'vite');
const entry = JSON.parse(readFileSync(path.join(vitePkg, 'package.json'), 'utf8')).exports['.'];
const file = typeof entry === 'string' ? entry : entry.import?.default ?? entry.import ?? entry.default;
const { build } = await import(pathToFileURL(path.join(vitePkg, file)).href);

const modules = new Set();
const assets = [];
const config = path.join(web, 'vite.config.ts');
await build({
  root: web,
  configFile: existsSync(config) ? config : false,
  logLevel: 'error',
  build: { outDir: path.join(out, 'bundle'), emptyOutDir: true },
  plugins: [
    {
      name: 'sitboard-notices',
      generateBundle(_options, bundle) {
        for (const id of this.getModuleIds()) {
          if (/\.(css|scss|sass|less|styl|pcss)($|\?)|[?&]type=style/.test(id)) modules.add(id);
        }
        for (const f of Object.values(bundle)) {
          if (f.type === 'chunk') {
            for (const [id, m] of Object.entries(f.modules)) if (m.renderedLength > 0) modules.add(id);
            continue;
          }
          if (f.type !== 'asset') continue;
          const sources = f.originalFileNames ?? (f.originalFileName ? [f.originalFileName] : []);
          assets.push({ fileName: f.fileName, sources: [...sources].sort() });
        }
      },
    },
  ],
});
assets.sort((a, b) => a.fileName.localeCompare(b.fileName));
writeFileSync(path.join(out, 'modules.json'), JSON.stringify({ modules: [...modules].sort(), assets }, null, 1) + '\n');

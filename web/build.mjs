// Builds the dashboard into internal/web/dist, which the Go binary embeds.
// The output is committed, so the build is deterministic: fixed file names,
// no content hashes, timestamps or source maps, and LF line endings.
import { copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const web = fileURLToPath(new URL('.', import.meta.url));
const dist = join(web, '..', 'internal', 'web', 'dist');

const fonts = [
  '@fontsource/inter/files/inter-latin-400-normal.woff2',
  '@fontsource/inter/files/inter-latin-500-normal.woff2',
  '@fontsource/inter/files/inter-latin-600-normal.woff2',
  '@fontsource/inter/files/inter-latin-700-normal.woff2',
  '@fontsource/jetbrains-mono/files/jetbrains-mono-latin-500-normal.woff2',
];

// The fonts are licensed under the SIL Open Font License, whose text must
// accompany every copy of them.
const fontLicenses = {
  'inter-LICENSE.txt': '@fontsource/inter/LICENSE',
  'jetbrains-mono-LICENSE.txt': '@fontsource/jetbrains-mono/LICENSE',
};

// app.js bundles these MIT-licensed packages, whose notices must accompany
// every copy of them. They export neither their LICENSE nor their
// package.json, so the files are read from the node_modules layout that
// package-lock.json pins.
const bundleLicenses = {
  'preact-LICENSE.txt': 'preact',
  'preact-signals-LICENSE.txt': '@preact/signals',
  'preact-signals-core-LICENSE.txt': '@preact/signals-core',
};

/** @param {string} specifier */
function resolvePackageFile(specifier) {
  return fileURLToPath(import.meta.resolve(specifier));
}

/**
 * @param {string} path
 * @param {string} text
 */
function writeText(path, text) {
  writeFileSync(path, text.replaceAll('\r\n', '\n'));
}

rmSync(dist, { recursive: true, force: true });
mkdirSync(join(dist, 'fonts'), { recursive: true });
mkdirSync(join(dist, 'licenses'), { recursive: true });

// The CSS that main.tsx imports comes out beside app.js as app.css.
await build({
  entryPoints: [join(web, 'src', 'main.tsx')],
  outfile: join(dist, 'app.js'),
  bundle: true,
  format: 'esm',
  target: 'es2022',
  minify: true,
  sourcemap: false,
  tsconfig: join(web, 'tsconfig.json'),
  // The stylesheet's font URLs name the copies made below.
  external: ['/fonts/*'],
  logLevel: 'warning',
});

for (const font of fonts) {
  copyFileSync(resolvePackageFile(font), join(dist, 'fonts', basename(font)));
}
for (const [name, license] of Object.entries(fontLicenses)) {
  writeText(join(dist, 'fonts', name), readFileSync(resolvePackageFile(license), 'utf8'));
}
for (const [name, pkg] of Object.entries(bundleLicenses)) {
  writeText(join(dist, 'licenses', name), readFileSync(join(web, 'node_modules', pkg, 'LICENSE'), 'utf8'));
}
writeText(join(dist, 'index.html'), readFileSync(join(web, 'index.html'), 'utf8'));

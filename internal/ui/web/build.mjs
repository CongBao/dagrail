import { build } from 'esbuild';
import { copyFile, readFile, writeFile } from 'node:fs/promises';

const common = { bundle: true, minify: true, sourcemap: false, target: ['es2022'], legalComments: 'none', charset: 'utf8' };
await build({ ...common, entryPoints: ['app.ts'], outfile: '../static/app.js', format: 'iife' });
// elk-worker.min.js is already the official dedicated-worker entry point. It
// implements ELK's register/layout message protocol directly; rebundling it
// changes its environment detection and breaks its Worker shim in Chromium.
await copyFile('node_modules/elkjs/lib/elk-worker.min.js', '../static/layout-worker.js');
const workerPath = '../static/layout-worker.js';
const worker = await readFile(workerPath, 'utf8');
await writeFile(workerPath, `${worker.replace(/[ \t]+$/gm, '').replace(/\n*$/, '')}\n`);

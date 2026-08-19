import {defineConfig} from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  fullyParallel: false,
  retries: 0,
  workers: 1,
  reporter: 'line',
  snapshotPathTemplate: '{testDir}/goldens/{arg}{ext}',
  expect: {toHaveScreenshot: {animations: 'disabled', maxDiffPixelRatio: 0.025}},
  use: {
    baseURL: 'http://127.0.0.1:41736',
    browserName: 'chromium',
    channel: 'chrome',
    colorScheme: 'light',
    locale: 'en-US',
    reducedMotion: 'reduce',
    timezoneId: 'UTC',
    viewport: {width: 1440, height: 900},
  },
  webServer: {
    command: 'node visual/fixture-server.mjs',
    url: 'http://127.0.0.1:41736/api/v1/head',
    reuseExistingServer: false,
    timeout: 15_000,
  },
});

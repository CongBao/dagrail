import {expect, test} from '@playwright/test';

test.beforeEach(async({page})=>{
  await page.addInitScript(()=>{
    localStorage.setItem('dagrail.theme','light');
    localStorage.setItem('dagrail.navigatorPinned','true');
    localStorage.setItem('dagrail.autoRefresh','0');
  });
  await page.goto('/');
  await expect(page.locator('#connection')).toHaveText('Connected');
  await expect(page.locator('#graph [data-ref="foundation"]')).toBeVisible();
});

test('project map and accordion retain a calm readable hierarchy',async({page})=>{
  // The collapsed map intentionally has horizontal overflow. Capture the fixed
  // viewport rather than OS-dependent full-page scrollbar geometry.
  await expect(page).toHaveScreenshot('project-map-light.png');

  await page.locator('#graph [data-ref="build"]').click();
  await expect(page.locator('#graph [data-ref="build-implement"]')).toBeVisible();
  await expect(page.locator('#graph .dag-group.expanded')).toHaveCount(1);
  await expect(page.locator('#graph [data-ref="build"]')).toHaveAttribute('aria-expanded','true');
  await expect(page).toHaveScreenshot('group-expanded-light.png');

  await page.locator('#graph [data-ref="release"]').click();
  await expect(page.locator('#graph [data-ref="release-prepare"]')).toBeVisible();
  await expect(page.locator('#graph [data-ref="build-implement"]')).toHaveCount(0);
  await expect(page.locator('#graph .dag-group.expanded')).toHaveCount(1);

  await page.locator('#theme').click();
  await expect(page.locator('html')).toHaveAttribute('data-resolved-theme','dark');
  await expect(page).toHaveScreenshot('group-expanded-dark.png');
});

test('search, node detail, reduced motion, and narrow layout remain connected',async({page})=>{
  await page.setViewportSize({width:720,height:760});
  await page.locator('#search').fill('implement');
  await expect(page.locator('.search-result').first()).toBeVisible();
  await page.locator('.search-result').first().click();
  await expect(page.locator('#inspector')).toHaveClass(/open/);
  await expect(page.locator('#inspector-title')).toHaveText('build-implement');
  await expect(page.locator('#inspector-content')).toContainText('Implement build');
  await expect(page.locator('#inspector')).toHaveCSS('position','fixed');
  await expect(page).toHaveScreenshot('narrow-node-inspector-light.png');
});

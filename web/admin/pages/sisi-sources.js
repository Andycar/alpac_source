// pages/sisi-sources.js — SISI source modules (adult balancers).
// Shares the jsmodules-shaped backend at /api/sisi-sources/*.
import { createJsModulesPage } from './_jsmodules.js';

const page = createJsModulesPage({
  apiRoot:       '/sisi-sources',
  title:         'SISI источники',
  installPrompt: 'URL манифеста SISI источника (.json):',
  emptyMsg:      'Нет SISI источников. Установите через URL или положите JSON в database/sisi_sources/.',
});

export const render = page.render;

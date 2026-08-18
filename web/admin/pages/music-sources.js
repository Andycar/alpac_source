// pages/music-sources.js — Music plugin sources (vk audiobot etc).
// Shares the jsmodules-shaped backend at /api/music-sources/*.
import { createJsModulesPage } from './_jsmodules.js';

const page = createJsModulesPage({
  apiRoot:       '/music-sources',
  title:         'Music источники',
  installPrompt: 'URL манифеста музыкального источника (.json):',
  emptyMsg:      'Нет музыкальных источников. Установите через URL или положите JSON в database/music_sources/.',
});

export const render = page.render;

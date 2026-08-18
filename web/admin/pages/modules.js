// pages/modules.js — JS modules registry (uses shared _jsmodules factory).
import { createJsModulesPage } from './_jsmodules.js';

const page = createJsModulesPage({
  apiRoot:       '/modules',
  title:         'JS модули',
  installPrompt: 'URL манифеста модуля (.json) или JS-источника:',
  emptyMsg:      'Нет установленных модулей. Нажмите «Установить» и вставьте URL.',
});

export const render = page.render;

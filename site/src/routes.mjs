export const pagePath = name => name === 'trunk' ? '/' : `/${name}`;
export const homeRedirects = ['/trunk', '/trunk/', '/trunk/index.html', '/trunk.html'];
export const redirects = [
 ...homeRedirects.map(from => ({from, to:'/', status:301})),
 ...['/invisible','/invisible/','/invisible/index.html'].map(from=>({from,to:'/hidden',status:302})),
 ...['/logger', '/logger/', '/logger/index.html', '/logger.html'].map(from => ({from, to:'/hidden#logger', status:302})),
 ...['snoot','seeds','bench'].flatMap(name=>[`/${name}`,`/${name}/`,`/${name}/index.html`,`/${name}.html`].map(from=>({from,to:`/#${name}`,status:302}))),
 ...['/performance','/performance/','/performance/index.html','/performance.html'].map(from=>({from,to:'/#bench',status:302})),
 ...['definitions','collections','messaging'].flatMap(name=>[`/seeds/${name}`,`/seeds/${name}/`,`/seeds/${name}/index.html`].map(from=>({from,to:`/hidden#seeds-${name}`,status:302})))
];

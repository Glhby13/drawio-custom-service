// DISPOSABLE SPIKE — NOT PRODUCTION CODE (SPIKE-001).
// Static server that plays the gateway's role for the experiment:
//   /                      -> host.html (the "custom" host page, same origin as the editor)
//   /editor/*              -> draw.io webapp (unmodified files from draw.war)
//   /editor/js/PreConfig.js-> generated like jgraph/docker-drawio docker-entrypoint.sh (default CSP meta,
//                             cloud integrations off, sync manual). ALLOW_PLUGINS=1 adds ALLOW_CUSTOM_PLUGINS.
//   /spike-plugin.js       -> test plugin
import http from 'node:http';
import { Buffer } from 'node:buffer';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
// Two backends for /editor/*: DRAWIO_WEBAPP (extracted draw.war, static) or DRAWIO_UPSTREAM (a running
// jgraph/drawio container, proxied as-is, e.g. http://127.0.0.1:8081 - the container must be started with
// DRAWIO_SERVER_URL=http://localhost:8790/editor/ so that Tomcat serves under /editor).
const webapp = process.env.DRAWIO_WEBAPP;
const upstream = process.env.DRAWIO_UPSTREAM ? new URL(process.env.DRAWIO_UPSTREAM) : null;
if (!upstream && (!webapp || !fs.existsSync(path.join(webapp, 'index.html')))) {
  console.error('set DRAWIO_WEBAPP (extracted draw.war) or DRAWIO_UPSTREAM (running jgraph/drawio container)');
  process.exit(1);
}

const types = { '.html': 'text/html', '.js': 'application/javascript', '.css': 'text/css', '.json': 'application/json',
  '.svg': 'image/svg+xml', '.png': 'image/png', '.gif': 'image/gif', '.ico': 'image/x-icon', '.xml': 'application/xml',
  '.txt': 'text/plain', '.woff2': 'font/woff2', '.woff': 'font/woff', '.ttf': 'font/ttf' };

// Same default CSP string as docker-entrypoint.sh (DRAWIO_CSP_HEADER unset, no GitLab).
const DEFAULT_CSP = "default-src 'self'; script-src 'self' https://storage.googleapis.com https://apis.google.com https://docs.google.com https://code.jquery.com 'unsafe-inline'; connect-src 'self' https://*.dropboxapi.com https://api.trello.com https://api.github.com https://raw.githubusercontent.com https://*.googleapis.com https://*.googleusercontent.com https://graph.microsoft.com https://*.1drv.com https://*.sharepoint.com https://gitlab.com https://*.google.com https://fonts.gstatic.com https://fonts.googleapis.com; img-src * data:; media-src * data:; font-src * about:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; frame-src 'self' https://*.google.com;";

function preConfig(base, allowPlugins) {
  return [
    '(function() {', '  try {', "    var s = document.createElement('meta');",
    `    s.setAttribute('content', ${JSON.stringify(DEFAULT_CSP)});`,
    "    s.setAttribute('http-equiv', 'Content-Security-Policy');",
    "    var t = document.getElementsByTagName('meta')[0];", '    t.parentNode.insertBefore(s, t);',
    '  } catch (e) {}', '})();',
    `window.DRAWIO_SERVER_URL = '${base}/editor/';`,
    `window.DRAWIO_BASE_URL = '${base}/editor';`,
    "window.DRAWIO_VIEWER_URL = '';", "window.DRAWIO_LIGHTBOX_URL = '';", "window.DRAW_MATH_URL = 'math4/es5';",
    'window.EXPORT_URL = null;', 'window.DRAWIO_CONFIG = null;',
    allowPlugins ? 'window.ALLOW_CUSTOM_PLUGINS = true;' : '',
    "urlParams['sync'] = 'manual';", "urlParams['db'] = '0';", "urlParams['gh'] = '0';", "urlParams['tr'] = '0';",
    "urlParams['gapi'] = '0';", "urlParams['od'] = '0';", "urlParams['gl'] = '0';", '',
  ].join('\n');
}

export function start(port, { allowPlugins = false } = {}) {
  const base = `http://localhost:${port}`;
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, base);
    let file;
    if (url.pathname === '/' || url.pathname === '/host.html') file = path.join(here, 'host.html');
    else if (url.pathname === '/spike-plugin.js') file = path.join(here, 'spike-plugin.js');
    else if (url.pathname === '/api/ping') { res.writeHead(200, { 'content-type': 'application/json' }); return res.end('{"ok":true}'); }
    else if (!upstream && url.pathname === '/editor/js/PreConfig.js') {
      res.writeHead(200, { 'content-type': 'application/javascript', 'cache-control': 'no-store' });
      return res.end(preConfig(base, allowPlugins));
    } else if (url.pathname === '/editor' ) { res.writeHead(302, { location: '/editor/' }); return res.end(); }
    else if (upstream && url.pathname.startsWith('/editor/')) {
      const preq = http.request({ host: upstream.hostname, port: upstream.port, path: req.url, method: req.method, headers: { ...req.headers, host: upstream.host } }, (pres) => {
        const isPre = url.pathname === '/editor/js/PreConfig.js';
        if (!isPre) { res.writeHead(pres.statusCode, { ...pres.headers, 'cache-control': 'no-store' }); return pres.pipe(res); }
        const chunks = [];
        pres.on('data', (c) => chunks.push(c));
        pres.on('end', () => {
          // the container's own PreConfig.js (real entrypoint output); only the origin is rewritten for the second
          // server instance and ALLOW_CUSTOM_PLUGINS appended for the plugin experiment.
          let body = Buffer.concat(chunks).toString('utf8').split('http://localhost:8790').join(base);
          if (allowPlugins) body += '\nwindow.ALLOW_CUSTOM_PLUGINS = true;\n';
          res.writeHead(200, { 'content-type': 'application/javascript', 'cache-control': 'no-store' });
          res.end(body);
        });
      });
      preq.on('error', (e) => { res.writeHead(502); res.end(String(e)); });
      return req.pipe(preq);
    } else if (url.pathname.startsWith('/editor/')) {
      const rel = decodeURIComponent(url.pathname.slice('/editor/'.length)) || 'index.html';
      file = path.resolve(webapp, rel);
      if (!file.startsWith(path.resolve(webapp) + path.sep)) { res.writeHead(403); return res.end(); }
    }
    if (!file || !fs.existsSync(file) || fs.statSync(file).isDirectory()) { res.writeHead(404); return res.end('not found'); }
    res.writeHead(200, { 'content-type': types[path.extname(file)] || 'application/octet-stream', 'cache-control': 'no-store' });
    fs.createReadStream(file).pipe(res);
  });
  return new Promise((resolve) => server.listen(port, '127.0.0.1', () => resolve(server)));
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const port = Number(process.env.PORT || 8790);
  start(port, { allowPlugins: process.env.ALLOW_PLUGINS === '1' }).then(() => console.log(`spike server http://localhost:${port}/`));
}

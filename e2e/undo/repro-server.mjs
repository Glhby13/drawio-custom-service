import http from 'node:http';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const REPO = path.resolve(decodeURIComponent(new URL('../..', import.meta.url).pathname));
const WEB = path.join(REPO, 'internal/edge/web');
const DRAWIO = process.env.DRAWIO_WEBAPP;
if (!DRAWIO || !fs.existsSync(path.join(DRAWIO, 'index.html'))) throw new Error('Set DRAWIO_WEBAPP to the official draw.io webapp directory');
const GOVAL_BIN = process.env.DRAWIO_VALIDATOR || path.join(REPO, 'e2e/undo/.validator');
const M = require(path.join(WEB, 'model.js'));
const B = require(path.join(WEB, 'bridge.js'));

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.gif': 'image/gif',
  '.ico': 'image/x-icon',
  '.xml': 'application/xml',
  '.txt': 'text/plain',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.ttf': 'font/ttf',
};

const EMPTY_XML = '<mxfile><diagram id="p1" name="Page-1"><mxGraphModel dx="1422" dy="762" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="850" pageHeight="1100"><root><mxCell id="0"/><mxCell id="1" parent="0"/></root></mxGraphModel></diagram></mxfile>';

function initDoc() {
  const d = M.newDoc();
  d.pages.set('p1', {
    id: 'p1',
    name: 'Page-1',
    order: '000000',
    attrs: '',
    cells: new Map([
      ['0', { id: '0', kind: '-', parent: '', order: '', props: {} }],
      ['1', { id: '1', kind: '-', parent: '0', order: '000000', props: {} }],
    ]),
  });
  return d;
}

function docStateJson(d) {
  return M.sortedPages(d).map((p) => ({
    id: p.id,
    name: p.name,
    order: p.order,
    attrs: p.attrs,
    cells: Array.from(p.cells.values()).map((c) => ({
      id: c.id, k: c.kind, p: c.parent, o: c.order, props: c.props,
    })),
  }));
}

function docExportXml(d) {
  const pages = M.sortedPages(d).map((p) => B.pageXml(p)).join('');
  return '<mxfile>' + pages + '</mxfile>';
}

function createGoValidator() {
  const proc = spawn(GOVAL_BIN, [], { stdio: ['pipe', 'pipe', 'inherit'] });
  let buf = '';
  const waiters = [];
  proc.stdout.on('data', (chunk) => {
    buf += chunk.toString('utf8');
    let nl;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl);
      buf = buf.slice(nl + 1);
      const w = waiters.shift();
      if (w) w.resolve(JSON.parse(line));
    }
  });
  let fatal = null;
  const fail = err => { fatal = err; for (const w of waiters.splice(0)) w.reject(err); };
  proc.on('error', fail);
  proc.on('exit', code => fail(new Error('Go validator exited: ' + code)));
  let chain = Promise.resolve();
  const call = (payload) => {
    const p = chain.then(() => new Promise((resolve, reject) => {
      if (fatal) { reject(fatal); return; }
      waiters.push({ resolve, reject });
      proc.stdin.write(JSON.stringify(payload) + '\n');
    }));
    chain = p.catch(() => {});
    return p;
  };
  return { call, close: () => proc.kill() };
}

function sendWsFrame(socket, str) {
  if (socket.destroyed) return;
  const payload = Buffer.from(str, 'utf8');
  const len = payload.length;
  let header;
  if (len < 126) {
    header = Buffer.from([0x81, len]);
  } else if (len < 65536) {
    header = Buffer.alloc(4);
    header[0] = 0x81; header[1] = 126; header.writeUInt16BE(len, 2);
  } else {
    header = Buffer.alloc(10);
    header[0] = 0x81; header[1] = 127; header.writeBigUInt64BE(BigInt(len), 2);
  }
  socket.write(Buffer.concat([header, payload]));
}

function decodeWsFrames(buf, onMsg, onClose) {
  let offset = 0;
  while (offset + 2 <= buf.length) {
    const b0 = buf[offset];
    const b1 = buf[offset + 1];
    const opcode = b0 & 0x0f;
    const masked = (b1 & 0x80) !== 0;
    let len = b1 & 0x7f;
    let pos = offset + 2;
    if (len === 126) {
      if (pos + 2 > buf.length) break;
      len = buf.readUInt16BE(pos); pos += 2;
    } else if (len === 127) {
      if (pos + 8 > buf.length) break;
      len = Number(buf.readBigUInt64BE(pos)); pos += 8;
    }
    if (masked) {
      if (pos + 4 > buf.length) break;
      const mask = buf.subarray(pos, pos + 4); pos += 4;
      if (pos + len > buf.length) break;
      const out = Buffer.allocUnsafe(len);
      for (let i = 0; i < len; i++) out[i] = buf[pos + i] ^ mask[i & 3];
      offset = pos + len;
      if (opcode === 8) {
        onClose(len >= 2 ? out.subarray(0, 2) : Buffer.from([0x03, 0xe8]));
      }
      else if (opcode === 1) onMsg(out.toString('utf8'));
    } else {
      if (pos + len > buf.length) break;
      const out = buf.subarray(pos, pos + len);
      offset = pos + len;
      if (opcode === 8) {
        onClose(len >= 2 ? out.subarray(0, 2) : Buffer.from([0x03, 0xe8]));
      }
      else if (opcode === 1) onMsg(out.toString('utf8'));
    }
  }
  return buf.subarray(offset);
}

export function startServer(port = 0, opts = {}) {
  const goval = createGoValidator();
  const docs = new Map();
  const getDoc = (id) => {
    if (!docs.has(id)) docs.set(id, { id, title: 'Repro Doc', doc: initDoc(), sessions: new Set(), seenBatches: new Map(), opLog: [], nacks: [] });
    return docs.get(id);
  };
  let sidCounter = 0;

  function preConfig(base) {
    return [
      `window.DRAWIO_SERVER_URL = '${base}/editor/';`,
      `window.DRAWIO_BASE_URL = '${base}/editor';`,
      `window.DRAWIO_VIEWER_URL = '';`,
      `window.DRAWIO_LIGHTBOX_URL = '';`,
      `window.DRAW_MATH_URL = 'math4/es5';`,
      `window.EXPORT_URL = null;`,
      `window.DRAWIO_CONFIG = null;`,
      `urlParams['sync'] = 'manual';`,
      `urlParams['db'] = '0'; urlParams['gh'] = '0'; urlParams['tr'] = '0';`,
      `urlParams['gapi'] = '0'; urlParams['od'] = '0'; urlParams['gl'] = '0';`,
    ].join('\n');
  }

  const server = http.createServer((req, res) => {
    const base = `http://127.0.0.1:${server.address().port}`;
    const u = new URL(req.url, base);
    const user = req.headers['x-test-user'] || u.searchParams.get('user') || 'alice';

    if (u.pathname === '/api/me') {
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ username: user, name: user, roles: ['user'], csrf: 'test-csrf', idp: 'local', features: { ai: false } }));
    }
    const mDoc = /^\/api\/v1\/documents\/([^/]+)$/.exec(u.pathname);
    if (mDoc && req.method === 'GET') {
      const d = getDoc(decodeURIComponent(mDoc[1]));
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ document: { id: d.id, title: d.title, headSeq: d.doc.seq }, role: 'owner', can: { edit: true, share: true, rename: true, delete: true } }));
    }
    if (u.pathname === '/' || /^\/d\/[^/]+$/.test(u.pathname)) {
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
      return res.end(fs.readFileSync(path.join(WEB, 'host.html')));
    }
    if (u.pathname.startsWith('/static/')) {
      const name = path.basename(u.pathname);
      const fp = path.join(WEB, name);
      if (!fs.existsSync(fp)) { res.writeHead(404); return res.end('not found'); }
      res.writeHead(200, { 'content-type': TYPES[path.extname(fp)] || 'application/octet-stream', 'cache-control': 'no-store' });
      return fs.createReadStream(fp).pipe(res);
    }
    if (u.pathname === '/editor') { res.writeHead(302, { location: '/editor/' }); return res.end(); }
    if (u.pathname === '/editor/js/PreConfig.js') {
      res.writeHead(200, { 'content-type': 'text/javascript; charset=utf-8', 'cache-control': 'no-store' });
      return res.end(preConfig(base));
    }
    if (u.pathname.startsWith('/editor/')) {
      const rel = decodeURIComponent(u.pathname.slice('/editor/'.length)) || 'index.html';
      const fp = path.resolve(DRAWIO, rel);
      if (!fp.startsWith(path.resolve(DRAWIO) + path.sep) || !fs.existsSync(fp) || fs.statSync(fp).isDirectory()) {
        res.writeHead(404); return res.end('not found');
      }
      res.writeHead(200, { 'content-type': TYPES[path.extname(fp)] || 'application/octet-stream', 'cache-control': 'no-store' });
      return fs.createReadStream(fp).pipe(res);
    }
    res.writeHead(404); res.end('not found');
  });

  const upgradedSockets = new Set();
  server.on('upgrade', (req, socket) => {
    upgradedSockets.add(socket);
    socket.on('close', () => upgradedSockets.delete(socket));
    const u = new URL(req.url, 'http://127.0.0.1');
    const m = /^\/ws\/v1\/documents\/([^/]+)$/.exec(u.pathname);
    if (!m) { socket.destroy(); return; }
    const docId = decodeURIComponent(m[1]);
    const key = req.headers['sec-websocket-key'];
    const accept = crypto.createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
    socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + '\r\n\r\n');

    const d = getDoc(docId);
    const sid = 's' + (++sidCounter);
    const sess = { sid, socket, send: (obj) => sendWsFrame(socket, JSON.stringify(obj)) };
    d.sessions.add(sess);

    let buf = Buffer.alloc(0);
    socket.on('data', async (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      buf = decodeWsFrames(buf, async (text) => {
        let msg;
        try { msg = JSON.parse(text); } catch { return; }
        if (msg.t === 'join') {
          await goval.call({ docId, op: 'init' });
          const since = Number(msg.since || 0);
          if (since > 0 && since <= d.doc.seq && d.doc.seq - since <= 2000) {
            const tail = d.opLog.filter((o) => o.seq > since);
            if (tail.length === d.doc.seq - since && !tail.some((o) => o.t === 'restore')) {
              sess.send({ t: 'joined', mode: 'catchup', seq: d.doc.seq, ops: tail, role: 'owner', title: d.title, you: sid });
              return;
            }
          }
          const hash = await M.hash(d.doc);
          sess.send({
            t: 'joined', mode: 'full', seq: d.doc.seq, hash,
            state: docStateJson(d.doc), xml: d.doc.seq === 0 ? EMPTY_XML : docExportXml(d.doc),
            role: 'owner', title: d.title, you: sid,
          });
        } else if (msg.t === 'ping') {
          sess.send({ t: 'pong' });
        } else if (msg.t === 'act') {
          const act = Object.assign({}, msg.act, { sid, user: sid, name: sid });
          for (const s of d.sessions) if (s !== sess) s.send({ t: 'act', act });
        } else if (msg.t === 'ops') {
          if (opts.onWsOps) opts.onWsOps(sid, msg);
          const delay = opts.wsDelayMs ? opts.wsDelayMs(sid, msg) : 0;
          const processOps = async () => {
            if (d.seenBatches.has(msg.batch)) {
              sess.send({ t: 'ack', batch: msg.batch, seqs: d.seenBatches.get(msg.batch) });
              return;
            }
            const gres = await goval.call({ docId, op: 'apply', ops: msg.ops || [] });
            if (!gres.ok) {
              const failed = (msg.ops || [])[gres.index || 0] || {};
              const pg = d.doc.pages.get(failed.page);
              const missing = id => !!id && !!pg && !pg.cells.has(id);
              const explainedDeleteConflict = gres.code === 'duplicate_cell' ? !!pg && pg.cells.has(failed.cell)
                : gres.code === 'cell_gone' ? missing(failed.cell)
                : gres.code === 'terminal_gone' && (missing(failed.term) || missing(failed.props?.src) || missing(failed.props?.tgt));
              d.nacks.push({ sid, batch: msg.batch, code: gres.code, msg: gres.msg, index: gres.index, ops: msg.ops, explainedDeleteConflict: !!explainedDeleteConflict });
              sess.send({ t: 'nack', batch: msg.batch, code: gres.code, msg: gres.msg, index: gres.index });
              return;
            }
            const seqs = [];
            const applied = [];
            for (const o of msg.ops || []) {
              const res = M.resolve(d.doc, o);
              res.seq = d.doc.seq + 1;
              res.batch = msg.batch;
              res.actor = sid;
              M.apply(d.doc, res);
              seqs.push(res.seq);
              applied.push(res);
              d.opLog.push(res);
            }
            d.seenBatches.set(msg.batch, seqs);
            const hash = await M.hash(d.doc);
            if (hash !== gres.hash) {
              throw new Error('HASH DIVERGENCE JS vs GO: ' + docId);
            }
            for (const s of d.sessions) {
              for (let i = 0; i < applied.length; i++) {
                s.send({ t: 'op', op: applied[i], hash: i === applied.length - 1 ? hash : undefined });
              }
            }
            sess.send({ t: 'ack', batch: msg.batch, seqs });
          };
          if (delay > 0) setTimeout(processOps, delay);
          else await processOps();
        }
      }, (codeBuf) => {
        d.sessions.delete(sess);
        if (!socket.destroyed && codeBuf) socket.write(Buffer.concat([Buffer.from([0x88, 2]), codeBuf]));
        socket.end();
      });
    });
    socket.on('close', () => d.sessions.delete(sess));
    socket.on('error', () => d.sessions.delete(sess));
  });

  const origClose = server.close.bind(server);
  server.close = (...args) => {
    for (const socket of upgradedSockets) socket.destroy();
    goval.close();
    return origClose(...args);
  };

  return new Promise((resolve) => server.listen(port, '127.0.0.1', () => resolve({ server, port: server.address().port, docs })));
}

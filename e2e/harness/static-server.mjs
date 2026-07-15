// Minimal static file server for the built web console (apps/web/dist).
// Node builtins only — no external deps, so the e2e harness stays lightweight.
//
//   node static-server.mjs <distDir> <port>
//
// Serves files under distDir; unknown paths fall back to index.html (SPA). The
// app reads its API base from the ?api= query param (see apps/web/src/config.ts),
// so the harness points it at the ephemeral Edge without any rebuild.

import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import { extname, join, normalize } from "node:path";

const distDir = process.argv[2];
const port = Number(process.argv[3] || 0);
if (!distDir) {
  console.error("static-server: missing distDir arg");
  process.exit(1);
}

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".wasm": "application/wasm",
};

async function tryFile(path) {
  try {
    const s = await stat(path);
    if (s.isFile()) return path;
  } catch {
    /* miss */
  }
  return null;
}

const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url, "http://localhost");
    // Strip the leading slash and normalize away any ".." traversal.
    let rel = normalize(decodeURIComponent(url.pathname)).replace(/^([/\\])+/, "");
    if (rel === "" || rel === ".") rel = "index.html";
    let file = await tryFile(join(distDir, rel));
    if (!file) file = await tryFile(join(distDir, "index.html")); // SPA fallback
    if (!file) {
      res.writeHead(404).end("not found");
      return;
    }
    const body = await readFile(file);
    res.writeHead(200, {
      "Content-Type": MIME[extname(file)] || "application/octet-stream",
      "Cache-Control": "no-store",
    });
    res.end(body);
  } catch (e) {
    res.writeHead(500).end(String(e));
  }
});

server.listen(port, "127.0.0.1", () => {
  const addr = server.address();
  // Emit the bound port so a parent can discover it if launched with port 0.
  console.log(`static-server: listening ${typeof addr === "object" ? addr.port : port}`);
});

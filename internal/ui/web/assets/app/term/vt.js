// Minimal VT/ANSI terminal emulator (enough for shells + many TUI apps like vim).
// No external deps.

const ESC = "\x1b";

function clamp(n, lo, hi) {
  n = Number(n) || 0;
  return Math.max(lo, Math.min(hi, n));
}

function parseParams(raw) {
  if (!raw) return [];
  return raw.split(/[;:]/).map((s) => (s === "" ? 0 : Number(s) || 0));
}

function color256(n) {
  n = Number(n) || 0;
  n = clamp(n, 0, 255);
  const base16 = [
    "#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
    "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff",
  ];
  if (n < 16) return base16[n];
  if (n >= 232) {
    const g = 8 + (n - 232) * 10;
    const h = g.toString(16).padStart(2, "0");
    return `#${h}${h}${h}`;
  }
  n -= 16;
  const b = n % 6; n = (n - b) / 6;
  const g = n % 6; n = (n - g) / 6;
  const r = n % 6;
  const cv = (v) => (v === 0 ? 0 : 55 + v * 40);
  const rr = cv(r).toString(16).padStart(2, "0");
  const gg = cv(g).toString(16).padStart(2, "0");
  const bb = cv(b).toString(16).padStart(2, "0");
  return `#${rr}${gg}${bb}`;
}

function rgbTo256(r, g, b) {
  r = clamp(Number(r) || 0, 0, 255);
  g = clamp(Number(g) || 0, 0, 255);
  b = clamp(Number(b) || 0, 0, 255);

  // Grayscale ramp 232..255 (plus extremes mapped to base16).
  if (r === g && g === b) {
    if (r < 8) return 0;
    if (r > 248) return 15;
    const idx = Math.round((r - 8) / 10);
    return 232 + clamp(idx, 0, 23);
  }

  const to6 = (v) => clamp(Math.round((v / 255) * 5), 0, 5);
  const rr = to6(r);
  const gg = to6(g);
  const bb = to6(b);
  return 16 + 36 * rr + 6 * gg + bb;
}

function attrKey(a) {
  return `${a.fg}|${a.bg}|${a.bold ? 1 : 0}|${a.underline ? 1 : 0}|${a.inverse ? 1 : 0}`;
}

export class VTerm {
  constructor(cols, rows, writeFn, opts = {}) {
    this.cols = Math.max(10, cols | 0);
    this.rows = Math.max(5, rows | 0);
    this.writeFn = typeof writeFn === "function" ? writeFn : () => {};
    this.maxScrollback = Number(opts.maxScrollback ?? 3000);
    this.theme = {
      fg: opts.fg || "#e7eefc",
      bg: opts.bg || "#050914",
      cursor: opts.cursor || "rgba(231,238,252,.65)",
      cursorOutline: opts.cursorOutline || "rgba(231,238,252,.35)",
    };

    this.reset();
  }

  reset() {
    this.cursorX = 0;
    this.cursorY = 0;
    this.savedCursor = { x: 0, y: 0 };
    this.scrollTop = 0;
    this.scrollBottom = this.rows - 1;
    this.wrap = true;
    // New line mode: treat LF as CRLF (common expectation in shells and many programs).
    // Can be toggled by ANSI mode 20 (SM/RM), but we still guard against bare LF.
    this.newlineMode = true;
    this._sawCR = false;
    this.cursorVisible = true;
    this.cursorKeysApp = false;
    this.bracketedPaste = false;
    this.altActive = false;

    this.attr = { fg: -1, bg: -1, bold: false, underline: false, inverse: false };
    this.main = this._newBuffer(this.cols, this.rows);
    this.alt = this._newBuffer(this.cols, this.rows);
    this.buf = this.main;
    this.scrollback = [];

    this._state = "n";
    this._csi = { prefix: "", raw: "" };
    this._osc = "";
    this._oscEsc = false;
  }

  _newBuffer(cols, rows) {
    const size = cols * rows;
    return {
      cols,
      rows,
      ch: new Array(size).fill(" "),
      a: new Array(size).fill(attrKey({ fg: -1, bg: -1, bold: false, underline: false, inverse: false })),
    };
  }

  _index(x, y) {
    return y * this.cols + x;
  }

  resize(cols, rows) {
    cols = Math.max(10, cols | 0);
    rows = Math.max(5, rows | 0);
    if (cols === this.cols && rows === this.rows) return;

    const oldCols = this.cols;
    const oldRows = this.rows;
    const oldMain = this.main;
    const oldAlt = this.alt;

    this.cols = cols;
    this.rows = rows;
    this.scrollTop = 0;
    this.scrollBottom = rows - 1;
    this.cursorX = clamp(this.cursorX, 0, cols - 1);
    this.cursorY = clamp(this.cursorY, 0, rows - 1);
    this.savedCursor = { x: clamp(this.savedCursor.x, 0, cols - 1), y: clamp(this.savedCursor.y, 0, rows - 1) };

    const copy = (src, dst) => {
      const minRows = Math.min(oldRows, rows);
      const minCols = Math.min(oldCols, cols);
      for (let y = 0; y < minRows; y++) {
        for (let x = 0; x < minCols; x++) {
          const si = y * oldCols + x;
          const di = y * cols + x;
          dst.ch[di] = src.ch[si];
          dst.a[di] = src.a[si];
        }
      }
    };

    this.main = this._newBuffer(cols, rows);
    this.alt = this._newBuffer(cols, rows);
    copy(oldMain, this.main);
    copy(oldAlt, this.alt);
    this.buf = this.altActive ? this.alt : this.main;
  }

  _clearLine(y, x0 = 0, x1 = this.cols - 1) {
    x0 = clamp(x0, 0, this.cols - 1);
    x1 = clamp(x1, 0, this.cols - 1);
    if (x1 < x0) [x0, x1] = [x1, x0];
    for (let x = x0; x <= x1; x++) {
      const i = this._index(x, y);
      this.buf.ch[i] = " ";
      this.buf.a[i] = attrKey({ fg: -1, bg: -1, bold: false, underline: false, inverse: false });
    }
  }

  _lineToString(buf, y) {
    let s = "";
    for (let x = 0; x < this.cols; x++) s += buf.ch[y * this.cols + x] || " ";
    return s;
  }

  _pushScrollbackLine(y) {
    if (this.altActive) return;
    if (this.buf !== this.main) return;
    // Only treat as scrollback when the scroll region is full screen (typical shell mode).
    if (!(this.scrollTop === 0 && this.scrollBottom === this.rows - 1)) return;
    const line = this._lineToString(this.main, y);
    this.scrollback.push(line);
    const max = Math.max(0, this.maxScrollback | 0);
    if (max > 0 && this.scrollback.length > max) {
      this.scrollback.splice(0, this.scrollback.length - max);
    }
  }

  _clearScreen(mode = 2) {
    if (mode === 2) {
      for (let y = 0; y < this.rows; y++) this._clearLine(y, 0, this.cols - 1);
      return;
    }
    if (mode === 0) {
      this._clearLine(this.cursorY, this.cursorX, this.cols - 1);
      for (let y = this.cursorY + 1; y < this.rows; y++) this._clearLine(y, 0, this.cols - 1);
      return;
    }
    if (mode === 1) {
      for (let y = 0; y < this.cursorY; y++) this._clearLine(y, 0, this.cols - 1);
      this._clearLine(this.cursorY, 0, this.cursorX);
    }
  }

  _scrollUp(n = 1) {
    n = clamp(n, 1, this.rows);
    const top = this.scrollTop;
    const bot = this.scrollBottom;
    const regionRows = bot - top + 1;
    n = clamp(n, 1, regionRows);
    for (let k = 0; k < n; k++) this._pushScrollbackLine(top + k);
    for (let y = top; y <= bot - n; y++) {
      for (let x = 0; x < this.cols; x++) {
        const dst = this._index(x, y);
        const src = this._index(x, y + n);
        this.buf.ch[dst] = this.buf.ch[src];
        this.buf.a[dst] = this.buf.a[src];
      }
    }
    for (let y = bot - n + 1; y <= bot; y++) this._clearLine(y);
  }

  _scrollDown(n = 1) {
    n = clamp(n, 1, this.rows);
    const top = this.scrollTop;
    const bot = this.scrollBottom;
    const regionRows = bot - top + 1;
    n = clamp(n, 1, regionRows);
    for (let y = bot; y >= top + n; y--) {
      for (let x = 0; x < this.cols; x++) {
        const dst = this._index(x, y);
        const src = this._index(x, y - n);
        this.buf.ch[dst] = this.buf.ch[src];
        this.buf.a[dst] = this.buf.a[src];
      }
    }
    for (let y = top; y < top + n; y++) this._clearLine(y);
  }

  _lineFeed() {
    if (this.cursorY === this.scrollBottom) {
      this._scrollUp(1);
    } else {
      this.cursorY = clamp(this.cursorY + 1, 0, this.rows - 1);
    }
  }

  _reverseIndex() {
    if (this.cursorY === this.scrollTop) {
      this._scrollDown(1);
    } else {
      this.cursorY = clamp(this.cursorY - 1, 0, this.rows - 1);
    }
  }

  _putChar(ch) {
    const i = this._index(this.cursorX, this.cursorY);
    this.buf.ch[i] = ch;
    this.buf.a[i] = attrKey(this.attr);
    this.cursorX += 1;
    if (this.cursorX >= this.cols) {
      if (this.wrap) {
        this.cursorX = 0;
        this._lineFeed();
      } else {
        this.cursorX = this.cols - 1;
      }
    }
  }

  _setAttrFromSGR(params) {
    if (!params.length) params = [0];
    let i = 0;
    while (i < params.length) {
      const p = params[i++] ?? 0;
      if (p === 0) {
        this.attr = { fg: -1, bg: -1, bold: false, underline: false, inverse: false };
      } else if (p === 1) this.attr.bold = true;
      else if (p === 22) this.attr.bold = false;
      else if (p === 4) this.attr.underline = true;
      else if (p === 24) this.attr.underline = false;
      else if (p === 7) this.attr.inverse = true;
      else if (p === 27) this.attr.inverse = false;
      else if (p === 39) this.attr.fg = -1;
      else if (p === 49) this.attr.bg = -1;
      else if (p >= 30 && p <= 37) this.attr.fg = p - 30;
      else if (p >= 40 && p <= 47) this.attr.bg = p - 40;
      else if (p >= 90 && p <= 97) this.attr.fg = 8 + (p - 90);
      else if (p >= 100 && p <= 107) this.attr.bg = 8 + (p - 100);
      else if (p === 38 || p === 48) {
        const isBg = p === 48;
        const mode = params[i++] ?? 0;
        if (mode === 5) {
          const n = params[i++] ?? 0;
          if (isBg) this.attr.bg = clamp(n, 0, 255);
          else this.attr.fg = clamp(n, 0, 255);
        } else if (mode === 2) {
          // 24-bit: 38;2;r;g;b (map to nearest xterm-256 color).
          const r = params[i++] ?? 0;
          const g = params[i++] ?? 0;
          const b = params[i++] ?? 0;
          const n = rgbTo256(r, g, b);
          if (isBg) this.attr.bg = n;
          else this.attr.fg = n;
        }
      }
    }
  }

  _enterAlt() {
    if (this.altActive) return;
    this.altActive = true;
    this.savedCursor = { x: this.cursorX, y: this.cursorY };
    this.buf = this.alt;
    this._clearScreen(2);
    this.cursorX = 0;
    this.cursorY = 0;
  }

  _exitAlt() {
    if (!this.altActive) return;
    this.altActive = false;
    this.buf = this.main;
    this.cursorX = clamp(this.savedCursor.x, 0, this.cols - 1);
    this.cursorY = clamp(this.savedCursor.y, 0, this.rows - 1);
  }

  getTotalLines() {
    if (this.altActive) return this.rows;
    return (this.scrollback?.length || 0) + this.rows;
  }

  getLineAbs(absLine) {
    absLine = Number(absLine) || 0;
    if (this.altActive) {
      const y = clamp(absLine, 0, this.rows - 1);
      return this._lineToString(this.buf, y);
    }
    const sb = this.scrollback?.length || 0;
    if (absLine < sb) return this.scrollback[absLine] || "";
    const y = absLine - sb;
    if (y < 0 || y >= this.rows) return "";
    return this._lineToString(this.main, y);
  }

  write(data) {
    if (data == null) return;
    const s = String(data);
    for (let i = 0; i < s.length; i++) {
      const ch = s[i];
      if (this._state === "n") {
        if (ch === ESC) { this._state = "e"; continue; }
        if (ch === "\n") {
          if (this.newlineMode || !this._sawCR) this.cursorX = 0;
          this._lineFeed();
          this._sawCR = false;
          continue;
        }
        if (ch === "\r") {
          this.cursorX = 0;
          this._sawCR = true;
          continue;
        }
        if (ch === "\b") { this.cursorX = clamp(this.cursorX - 1, 0, this.cols - 1); continue; }
        if (ch === "\t") {
          const next = (Math.floor(this.cursorX / 8) + 1) * 8;
          this.cursorX = clamp(next, 0, this.cols - 1);
          continue;
        }
        if (ch === "\x07") { continue; } // BEL
        // Printable
        this._putChar(ch);
        this._sawCR = false;
        continue;
      }

      if (this._state === "e") {
        if (ch === "[") { this._state = "c"; this._csi = { prefix: "", raw: "" }; continue; }
        if (ch === "]") { this._state = "o"; this._osc = ""; this._oscEsc = false; continue; }
        if (ch === "7") { this.savedCursor = { x: this.cursorX, y: this.cursorY }; this._state = "n"; continue; }
        if (ch === "8") { this.cursorX = this.savedCursor.x; this.cursorY = this.savedCursor.y; this._state = "n"; continue; }
        if (ch === "D") { this._lineFeed(); this._state = "n"; continue; } // IND
        if (ch === "M") { this._reverseIndex(); this._state = "n"; continue; } // RI
        if (ch === "E") { this.cursorX = 0; this._lineFeed(); this._state = "n"; continue; } // NEL
        if (ch === "c") { this.reset(); this._state = "n"; continue; } // RIS
        if (ch === "=") { /* keypad app */ this._state = "n"; continue; }
        if (ch === ">") { /* keypad normal */ this._state = "n"; continue; }
        this._state = "n";
        continue;
      }

      if (this._state === "o") {
        // OSC ... BEL or ESC \
        if (this._oscEsc) {
          this._oscEsc = false;
          if (ch === "\\") { this._state = "n"; continue; }
          // Not a terminator, keep.
          this._osc += ESC + ch;
          continue;
        }
        if (ch === "\x07") { this._state = "n"; continue; }
        if (ch === ESC) { this._oscEsc = true; continue; }
        this._osc += ch;
        continue;
      }

      if (this._state === "c") {
        // CSI: optional prefix like ? then params then final.
        if (this._csi.raw.length === 0 && (ch === "?" || ch === ">" || ch === "=")) {
          this._csi.prefix = ch;
          continue;
        }
        const code = ch.charCodeAt(0);
        const isFinal = code >= 0x40 && code <= 0x7e;
        if (!isFinal) {
          this._csi.raw += ch;
          continue;
        }
        const raw = this._csi.raw;
        const prefix = this._csi.prefix;
        this._state = "n";
        this._csi = { prefix: "", raw: "" };

        const params = parseParams(raw);
        const p1 = params[0] ?? 0;

        if (ch === "A") this.cursorY = clamp(this.cursorY - (p1 || 1), 0, this.rows - 1);
        else if (ch === "B") this.cursorY = clamp(this.cursorY + (p1 || 1), 0, this.rows - 1);
        else if (ch === "C") this.cursorX = clamp(this.cursorX + (p1 || 1), 0, this.cols - 1);
        else if (ch === "D") this.cursorX = clamp(this.cursorX - (p1 || 1), 0, this.cols - 1);
        else if (ch === "G") this.cursorX = clamp((p1 || 1) - 1, 0, this.cols - 1); // CHA
        else if (ch === "d") this.cursorY = clamp((p1 || 1) - 1, 0, this.rows - 1); // VPA
        else if (ch === "E") { // CNL
          this.cursorY = clamp(this.cursorY + (p1 || 1), 0, this.rows - 1);
          this.cursorX = 0;
        } else if (ch === "F") { // CPL
          this.cursorY = clamp(this.cursorY - (p1 || 1), 0, this.rows - 1);
          this.cursorX = 0;
        } else if (ch === "a") this.cursorX = clamp(this.cursorX + (p1 || 1), 0, this.cols - 1); // HPR
        else if (ch === "e") this.cursorY = clamp(this.cursorY + (p1 || 1), 0, this.rows - 1); // VPR
        else if (ch === "H" || ch === "f") {
          const row = (params[0] || 1) - 1;
          const col = (params[1] || 1) - 1;
          this.cursorY = clamp(row, 0, this.rows - 1);
          this.cursorX = clamp(col, 0, this.cols - 1);
        } else if (ch === "J") {
          this._clearScreen(p1 || 0);
        } else if (ch === "K") {
          const mode = p1 || 0;
          if (mode === 0) this._clearLine(this.cursorY, this.cursorX, this.cols - 1);
          else if (mode === 1) this._clearLine(this.cursorY, 0, this.cursorX);
          else if (mode === 2) this._clearLine(this.cursorY, 0, this.cols - 1);
        } else if (ch === "m") {
          this._setAttrFromSGR(params);
        } else if (ch === "s") {
          this.savedCursor = { x: this.cursorX, y: this.cursorY };
        } else if (ch === "u") {
          this.cursorX = clamp(this.savedCursor.x, 0, this.cols - 1);
          this.cursorY = clamp(this.savedCursor.y, 0, this.rows - 1);
        } else if (ch === "I") {
          // CHT (tab forward)
          const n = p1 || 1;
          for (let k = 0; k < n; k++) {
            const next = (Math.floor(this.cursorX / 8) + 1) * 8;
            this.cursorX = clamp(next, 0, this.cols - 1);
          }
        } else if (ch === "Z") {
          // CBT (tab backward)
          const n = p1 || 1;
          for (let k = 0; k < n; k++) {
            const prev = Math.max(0, (Math.floor((this.cursorX - 1) / 8)) * 8);
            this.cursorX = clamp(prev, 0, this.cols - 1);
          }
        } else if (ch === "r") {
          const top = (params[0] || 1) - 1;
          const bot = (params[1] || this.rows) - 1;
          this.scrollTop = clamp(Math.min(top, bot), 0, this.rows - 1);
          this.scrollBottom = clamp(Math.max(top, bot), 0, this.rows - 1);
          this.cursorX = 0;
          this.cursorY = 0;
        } else if (ch === "L") {
          const n = p1 || 1;
          // Insert lines at cursor within scroll region.
          const y = this.cursorY;
          if (y >= this.scrollTop && y <= this.scrollBottom) {
            for (let k = 0; k < n; k++) {
              for (let yy = this.scrollBottom; yy > y; yy--) {
                for (let x = 0; x < this.cols; x++) {
                  const dst = this._index(x, yy);
                  const src = this._index(x, yy - 1);
                  this.buf.ch[dst] = this.buf.ch[src];
                  this.buf.a[dst] = this.buf.a[src];
                }
              }
              this._clearLine(y);
            }
          }
        } else if (ch === "M") {
          const n = p1 || 1;
          const y = this.cursorY;
          if (y >= this.scrollTop && y <= this.scrollBottom) {
            for (let k = 0; k < n; k++) {
              for (let yy = y; yy < this.scrollBottom; yy++) {
                for (let x = 0; x < this.cols; x++) {
                  const dst = this._index(x, yy);
                  const src = this._index(x, yy + 1);
                  this.buf.ch[dst] = this.buf.ch[src];
                  this.buf.a[dst] = this.buf.a[src];
                }
              }
              this._clearLine(this.scrollBottom);
            }
          }
        } else if (ch === "@") {
          const n = p1 || 1;
          const y = this.cursorY;
          for (let x = this.cols - 1; x >= this.cursorX + n; x--) {
            const dst = this._index(x, y);
            const src = this._index(x - n, y);
            this.buf.ch[dst] = this.buf.ch[src];
            this.buf.a[dst] = this.buf.a[src];
          }
          for (let x = this.cursorX; x < Math.min(this.cols, this.cursorX + n); x++) {
            const idx = this._index(x, y);
            this.buf.ch[idx] = " ";
            this.buf.a[idx] = attrKey(this.attr);
          }
        } else if (ch === "P") {
          const n = p1 || 1;
          const y = this.cursorY;
          for (let x = this.cursorX; x < this.cols - n; x++) {
            const dst = this._index(x, y);
            const src = this._index(x + n, y);
            this.buf.ch[dst] = this.buf.ch[src];
            this.buf.a[dst] = this.buf.a[src];
          }
          for (let x = this.cols - n; x < this.cols; x++) {
            const idx = this._index(x, y);
            this.buf.ch[idx] = " ";
            this.buf.a[idx] = attrKey(this.attr);
          }
        } else if (ch === "X") {
          const n = p1 || 1;
          const y = this.cursorY;
          for (let x = this.cursorX; x < Math.min(this.cols, this.cursorX + n); x++) {
            const idx = this._index(x, y);
            this.buf.ch[idx] = " ";
            this.buf.a[idx] = attrKey(this.attr);
          }
        } else if (ch === "S") {
          this._scrollUp(p1 || 1);
        } else if (ch === "T") {
          this._scrollDown(p1 || 1);
        } else if (ch === "n" && prefix === "" && p1 === 6) {
          // DSR: report cursor position (1-based).
          this.writeFn(`${ESC}[${this.cursorY + 1};${this.cursorX + 1}R`);
        } else if (ch === "n" && prefix === "" && p1 === 5) {
          // DSR: "OK" (ready).
          this.writeFn(`${ESC}[0n`);
        } else if (ch === "c" && prefix === "") {
          // DA: device attributes (best-effort VT100).
          this.writeFn(`${ESC}[?1;0c`);
        } else if ((ch === "h" || ch === "l") && prefix === "") {
          // SM/RM (non-DEC private).
          const set = ch === "h";
          for (const m of params) {
            if (m === 20) this.newlineMode = set;
          }
        } else if ((ch === "h" || ch === "l") && prefix === "?") {
          const set = ch === "h";
          for (const m of params) {
            if (m === 1) this.cursorKeysApp = set;
            else if (m === 7) this.wrap = set;
            else if (m === 25) this.cursorVisible = set;
            else if (m === 1049) set ? this._enterAlt() : this._exitAlt();
            else if (m === 1047) set ? this._enterAlt() : this._exitAlt();
            else if (m === 1048) {
              // save/restore cursor (often paired with alt buffer).
              if (set) this.savedCursor = { x: this.cursorX, y: this.cursorY };
              else { this.cursorX = this.savedCursor.x; this.cursorY = this.savedCursor.y; }
            } else if (m === 2004) this.bracketedPaste = set;
          }
        }
      }
    }
  }

  keyBytes(e) {
    // Return bytes to send for a keyboard event, considering terminal modes.
    if (e.metaKey) return null;

    // Ctrl+letter -> control codes
    if (e.ctrlKey && !e.altKey) {
      const k = e.key;
      if (k === " ") return new Uint8Array([0x00]);
      if (k.length === 1) {
        const code = k.toUpperCase().charCodeAt(0);
        if (code >= 0x40 && code <= 0x5f) return new Uint8Array([code - 0x40]);
      }
    }

    const enc = (s) => new TextEncoder().encode(s);
    if (e.key === "Enter") return enc("\r");
    if (e.key === "Backspace") return new Uint8Array([0x7f]);
    if (e.key === "Tab") return enc("\t");
    if (e.key === "Escape") return enc("\x1b");

    const app = this.cursorKeysApp;
    if (e.key === "ArrowUp") return enc(app ? "\x1bOA" : "\x1b[A");
    if (e.key === "ArrowDown") return enc(app ? "\x1bOB" : "\x1b[B");
    if (e.key === "ArrowRight") return enc(app ? "\x1bOC" : "\x1b[C");
    if (e.key === "ArrowLeft") return enc(app ? "\x1bOD" : "\x1b[D");
    if (e.key === "Home") return enc(app ? "\x1bOH" : "\x1b[H");
    if (e.key === "End") return enc(app ? "\x1bOF" : "\x1b[F");
    if (e.key === "Delete") return enc("\x1b[3~");
    if (e.key === "PageUp") return enc("\x1b[5~");
    if (e.key === "PageDown") return enc("\x1b[6~");

    if (e.key.length === 1 && !e.ctrlKey && !e.altKey) return enc(e.key);
    return null;
  }

  pasteBytes(text) {
    const enc = (s) => new TextEncoder().encode(s);
    if (!text) return null;
    if (this.bracketedPaste) return enc(`${ESC}[200~${text}${ESC}[201~`);
    return enc(text);
  }

  render(ctx, cellW, cellH, blinkOn, opts = {}) {
    const viewStartAbs = Number(opts.viewStartAbs ?? Math.max(0, this.getTotalLines() - this.rows));
    const showCursor = opts.showCursor !== false;

    const w = ctx.canvas.width;
    const h = ctx.canvas.height;
    ctx.clearRect(0, 0, w, h);

    ctx.fillStyle = this.theme.bg;
    ctx.fillRect(0, 0, w, h);

    ctx.textBaseline = "top";
    ctx.font = ctx.font || `${Math.floor(cellH * 0.92)}px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace`;

    const defaultFg = this.theme.fg;
    const defaultBg = this.theme.bg;

    for (let y = 0; y < this.rows; y++) {
      const absLine = viewStartAbs + y;
      let x = 0;
      while (x < this.cols) {
        let idx = null;
        let key = null;
        let sbLine = null;
        if (!this.altActive) {
          const sbLen = this.scrollback?.length || 0;
          if (absLine < sbLen) {
            sbLine = this.scrollback[absLine] || "";
            key = attrKey({ fg: -1, bg: -1, bold: false, underline: false, inverse: false });
          } else {
            const yy = absLine - sbLen;
            idx = this._index(x, clamp(yy, 0, this.rows - 1));
            key = this.main.a[idx];
          }
        } else {
          idx = this._index(x, absLine);
          key = this.buf.a[idx];
        }
        let runEnd = x + 1;
        if (sbLine != null) {
          while (runEnd < this.cols) runEnd++;
        } else {
          while (runEnd < this.cols) {
            const j = this.altActive ? this.buf.a[this._index(runEnd, absLine)] : this.main.a[this._index(runEnd, absLine - (this.scrollback?.length || 0))];
            if (j !== key) break;
            runEnd++;
          }
        }

        // Decode attr key
        const [fgS, bgS, boldS, underlineS, inverseS] = key.split("|");
        const fgN = Number(fgS);
        const bgN = Number(bgS);
        const bold = boldS === "1";
        const underline = underlineS === "1";
        const inverse = inverseS === "1";

        let fg = fgN >= 0 ? color256(fgN) : defaultFg;
        let bg = bgN >= 0 ? color256(bgN) : defaultBg;
        if (inverse) [fg, bg] = [bg, fg];

        // Background for the run (only if differs from default to reduce work).
        if (bg !== defaultBg) {
          ctx.fillStyle = bg;
          ctx.fillRect(x * cellW, y * cellH, (runEnd - x) * cellW, cellH);
        }

        // Text
        let text = "";
        if (sbLine != null) {
          for (let xx = x; xx < runEnd; xx++) text += sbLine[xx] || " ";
        } else if (this.altActive) {
          for (let xx = x; xx < runEnd; xx++) text += this.buf.ch[this._index(xx, absLine)] || " ";
        } else {
          const yy = absLine - (this.scrollback?.length || 0);
          for (let xx = x; xx < runEnd; xx++) text += this.main.ch[this._index(xx, yy)] || " ";
        }
        if (text.trimEnd() !== "" || fg !== defaultFg) {
          ctx.fillStyle = fg;
          ctx.font = `${bold ? "700 " : ""}${Math.floor(cellH * 0.92)}px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace`;
          ctx.fillText(text, x * cellW, y * cellH);
          if (underline) {
            ctx.strokeStyle = fg;
            ctx.lineWidth = Math.max(1, Math.floor(cellH / 14));
            const uy = (y + 1) * cellH - Math.max(2, Math.floor(cellH / 6));
            ctx.beginPath();
            ctx.moveTo(x * cellW, uy);
            ctx.lineTo(runEnd * cellW, uy);
            ctx.stroke();
          }
        }

        x = runEnd;
      }
    }

    // Cursor
    const bottomStart = Math.max(0, this.getTotalLines() - this.rows);
    const canShowCursor = showCursor && viewStartAbs === bottomStart;
    if (this.cursorVisible && blinkOn && canShowCursor) {
      const cx = this.cursorX * cellW;
      const cy = this.cursorY * cellH;
      ctx.fillStyle = this.theme.cursor;
      ctx.fillRect(cx, cy, cellW, cellH);
      ctx.strokeStyle = this.theme.cursorOutline;
      ctx.lineWidth = 1;
      ctx.strokeRect(cx + 0.5, cy + 0.5, cellW - 1, cellH - 1);
    }
  }
}

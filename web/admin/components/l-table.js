// <l-table> — premium table with sticky head, hover row, optional zebra,
// and column-driven cell formatting.
//
// Properties:
//   columns  Array<{key, label, align?: 'l'|'r'|'c', width?, format?, cell?}>
//            `format`: 'int' | 'ms' | 'bytes' | 'date' | 'pct'
//            `cell`:   function(row, col) -> string | TemplateResult
//   rows     Array<object>
//   loading  bool
//   empty    string — placeholder when zero rows
//
// Sortable columns: set sortable: true on a column to enable click-to-sort.
//
// No virtualisation yet — that's a Sprint-2-of-v2 problem. Today's lampac
// admin tables top out around 200 rows so this is fine.

import { LitElement, html, css, classMap } from './_lit.js';

class LTable extends LitElement {
  static properties = {
    columns: { type: Array },
    rows:    { type: Array },
    loading: { type: Boolean, reflect: true },
    empty:   { type: String },
    _sortKey: { state: true },
    _sortDir: { state: true },
  };

  constructor() {
    super();
    this.columns = [];
    this.rows = [];
    this.loading = false;
    this.empty = 'Нет данных';
    this._sortKey = '';
    this._sortDir = 'asc';
  }

  _sort(col) {
    if (!col.sortable) return;
    if (this._sortKey === col.key) {
      this._sortDir = this._sortDir === 'asc' ? 'desc' : 'asc';
    } else {
      this._sortKey = col.key;
      this._sortDir = 'asc';
    }
  }

  _sortedRows() {
    if (!this._sortKey) return this.rows;
    const dir = this._sortDir === 'asc' ? 1 : -1;
    return [...this.rows].sort((a, b) => {
      const av = a[this._sortKey];
      const bv = b[this._sortKey];
      if (av == null) return 1;
      if (bv == null) return -1;
      if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * dir;
      return String(av).localeCompare(String(bv), undefined, { numeric: true }) * dir;
    });
  }

  render() {
    const rows = this._sortedRows();
    return html`
      <div class="wrap">
        <table>
          <thead>
            <tr>${this.columns.map(c => html`
              <th
                class="${classMap({ sortable: c.sortable, asc: this._sortKey===c.key && this._sortDir==='asc', desc: this._sortKey===c.key && this._sortDir==='desc' })}"
                style=${alignAttr(c) + widthAttr(c)}
                @click=${() => this._sort(c)}
              >
                <span class="th-label">${c.label}</span>
                ${c.sortable ? html`<span class="th-arrow">↕</span>` : ''}
              </th>`)}</tr>
          </thead>
          <tbody>
            ${rows.length === 0 ? html`
              <tr><td class="empty" colspan="${this.columns.length}">${this.loading ? 'Загрузка…' : this.empty}</td></tr>
            ` : rows.map(r => html`
              <tr>${this.columns.map(c => html`
                <td style=${alignAttr(c)}>${renderCell(r, c)}</td>
              `)}</tr>
            `)}
          </tbody>
        </table>
      </div>
    `;
  }

  static styles = css`
    :host { display: block; }
    .wrap {
      background: var(--bg-1);
      border: 1px solid var(--border-1);
      border-radius: var(--r-3);
      overflow: hidden;
      box-shadow: var(--shadow-md);
    }
    table {
      width: 100%;
      border-collapse: collapse;
      font-size: var(--fs-sm);
    }
    thead { background: var(--bg-2); }
    th, td {
      padding: var(--s-3) var(--s-4);
      text-align: left;
      vertical-align: middle;
    }
    th {
      font-size: var(--fs-xs);
      font-weight: var(--fw-semibold);
      letter-spacing: 0.06em;
      text-transform: uppercase;
      color: var(--text-2);
      border-bottom: 1px solid var(--border-1);
      position: sticky;
      top: 0;
      user-select: none;
    }
    th.sortable { cursor: pointer; }
    th.sortable:hover { color: var(--text-0); background: var(--bg-3); }
    th .th-arrow { margin-left: 4px; opacity: 0.4; font-size: 10px; }
    th.asc .th-arrow::after { content: '↑'; }
    th.desc .th-arrow::after { content: '↓'; }
    th.asc .th-arrow,
    th.desc .th-arrow { opacity: 1; color: var(--accent); }
    th.asc .th-arrow::before,
    th.desc .th-arrow::before { content: ''; }

    tbody tr { transition: background var(--t-fast) var(--ease-out); }
    tbody tr:hover { background: var(--bg-2); }
    tbody td { color: var(--text-1); border-bottom: 1px solid var(--border-1); }
    tbody tr:last-child td { border-bottom: none; }

    td.empty {
      text-align: center;
      color: var(--text-3);
      padding: var(--s-6) var(--s-4);
    }
  `;
}

function alignAttr(col) {
  switch (col.align) {
    case 'r': return 'text-align:right;';
    case 'c': return 'text-align:center;';
    default:  return '';
  }
}
function widthAttr(col) {
  return col.width ? `width:${typeof col.width === 'number' ? col.width + 'px' : col.width};` : '';
}

function renderCell(row, col) {
  if (typeof col.cell === 'function') return col.cell(row, col);
  const v = row[col.key];
  switch (col.format) {
    case 'ms':    return Number.isFinite(v) ? Math.round(v) + 'ms' : '—';
    case 'int':   return Number.isFinite(v) ? Math.round(v).toLocaleString() : '—';
    case 'pct':   return Number.isFinite(v) ? (v * 100).toFixed(1) + '%' : '—';
    case 'date':  return v ? new Date(v).toLocaleString() : '—';
    case 'bytes': return Number.isFinite(v) ? formatBytes(v) : '—';
    default:      return v == null ? '—' : v;
  }
}
function formatBytes(n) {
  const units = ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return n.toFixed(1) + ' ' + units[i];
}

customElements.define('l-table', LTable);

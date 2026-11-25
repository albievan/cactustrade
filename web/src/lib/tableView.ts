// Presentation helpers for a table the viewer can filter, group and sort by any column.
// Nothing here decides anything about trading: it only rearranges rows the server sent.

export type Column<T> = {
  key: string;
  label: string;
  // The text a row shows (and is filtered and grouped by) in this column.
  text: (row: T) => string;
  // A value to sort by when the text would sort wrongly (numbers, times).
  sort?: (row: T) => number | string;
  // "choice": filter with a dropdown of the values present; "text": a contains-search.
  filter?: "choice" | "text";
  // Whether the column can group rows.
  group?: boolean;
  align?: "end";
  // A column that is not shown but can still be searched and grouped by (e.g. the day of a time).
  hidden?: boolean;
};

export type Filters = Record<string, string>;
export type Sort = { key: string; desc: boolean } | null;

export function applyFilters<T>(rows: T[], cols: Column<T>[], filters: Filters, search: string): T[] {
  const q = search.trim().toLowerCase();
  return rows.filter((r) => {
    for (const c of cols) {
      const f = filters[c.key];
      if (!f) continue;
      const v = c.text(r);
      if (c.filter === "choice" ? v !== f : !v.toLowerCase().includes(f.toLowerCase())) return false;
    }
    return !q || cols.some((c) => c.text(r).toLowerCase().includes(q));
  });
}

export function sortRows<T>(rows: T[], cols: Column<T>[], sort: Sort): T[] {
  if (!sort) return rows;
  const c = cols.find((x) => x.key === sort.key);
  if (!c) return rows;
  const val = c.sort ?? c.text;
  const out = [...rows].sort((a, b) => {
    const x = val(a), y = val(b);
    const d = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y), undefined, { numeric: true });
    return sort.desc ? -d : d;
  });
  return out;
}

// choices lists the distinct values of a column, in the order they first appear.
export function choices<T>(rows: T[], col: Column<T>): string[] {
  return [...new Set(rows.map(col.text))].sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
}

export type Group<T> = { value: string; rows: T[] };

// groupRows keeps the groups in the order their first row appears (so they follow the sort).
export function groupRows<T>(rows: T[], col: Column<T> | undefined): Group<T>[] {
  if (!col) return [{ value: "", rows }];
  const m = new Map<string, T[]>();
  for (const r of rows) {
    const k = col.text(r);
    const g = m.get(k);
    if (g) g.push(r);
    else m.set(k, [r]);
  }
  return [...m].map(([value, rs]) => ({ value, rows: rs }));
}

// nextSort cycles a column through ascending, descending and back to the default order.
export function nextSort(cur: Sort, key: string): Sort {
  if (!cur || cur.key !== key) return { key, desc: false };
  return cur.desc ? null : { key, desc: true };
}

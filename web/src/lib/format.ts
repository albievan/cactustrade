// Presentation formats shared by the pages (no business logic).

const two = (n: number) => String(n).padStart(2, "0");

// dateTime is a time as the full local date and the hour and minute: "26/11/2026 20:50".
export function dateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return `${two(d.getDate())}/${two(d.getMonth() + 1)}/${d.getFullYear()} ${two(d.getHours())}:${two(d.getMinutes())}`;
}

// hourMinute is the local "20:50" of a time.
export function hourMinute(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return `${two(d.getHours())}:${two(d.getMinutes())}`;
}

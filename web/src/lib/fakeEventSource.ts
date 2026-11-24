// A fake EventSource for the tests (jsdom has none): it records every stream the page opens
// and lets a test send named messages or fail the connection.
export class FakeEventSource extends EventTarget {
  static all: FakeEventSource[] = [];
  onerror: ((e: Event) => void) | null = null;
  closed = false;
  constructor(public url: string) {
    super();
    FakeEventSource.all.push(this);
  }
  close() {
    this.closed = true;
  }
  // send delivers one message (event name and JSON data) as the server would.
  send(name: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(name, { data: JSON.stringify(data) }));
  }
  // fail reports a broken connection.
  fail() {
    this.onerror?.(new Event("error"));
  }
  static latest(): FakeEventSource {
    const es = FakeEventSource.all[FakeEventSource.all.length - 1];
    if (!es) throw new Error("no EventSource was opened");
    return es;
  }
  static open(): FakeEventSource[] {
    return FakeEventSource.all.filter((e) => !e.closed);
  }
  static reset() {
    FakeEventSource.all = [];
  }
}

// Normalize after joining chunks: a network read can split a CRLF pair.
// Keeping a trailing partial event also makes UTF-8-decoded SSE lossless.
export function splitDesktopEvents(previous: string, chunk: string) {
  let pending = (previous + chunk).replace(/\r\n/g, "\n");
  if (pending.length > 3 * 1024 * 1024)
    throw new Error("Live event exceeded the display limit.");
  const data: string[] = [];
  let end: number;
  while ((end = pending.indexOf("\n\n")) >= 0) {
    const event = pending.slice(0, end);
    pending = pending.slice(end + 2);
    const value = event
      .split("\n")
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice(5).replace(/^ /, ""))
      .join("\n");
    if (value) data.push(value);
  }
  return { pending, data };
}

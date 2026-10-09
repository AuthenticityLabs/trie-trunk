import { Dictionaries } from "./dictionary.ts";

let sequence = 0;
const readers = new Map();

export function identity(
  millis = Date.parse("2026-09-25T00:00:01Z"),
  suffix = sequence++,
) {
  const bytes = Buffer.alloc(16);
  bytes.writeUIntBE(millis, 0, 6);
  bytes[6] = 0x70;
  bytes[8] = 0x80;
  bytes.writeUInt32BE(suffix, 12);
  return bytes.toString("base64");
}

export function eventOf(values, tap = "test") {
  const names = Object.keys(values).filter(
    (name) => name !== "id" && name !== "time",
  );
  const signature = JSON.stringify([tap, names]);
  let reader = readers.get(signature);
  if (!reader) {
    const dictionary = {
      version: 1,
      timezone: "UTC",
      fields: { 0: { name: "id", encoding: "uuidv7" } },
    };
    names.forEach((name, index) => (dictionary.fields[index + 1] = { name }));
    reader = new Dictionaries();
    reader.install({
      negotiation: "1".repeat(64),
      schemas: [
        {
          tap,
          version: 1,
          digest: readers.size.toString(16).padStart(64, "0"),
          dictionary,
        },
      ],
    });
    reader.read({ schema: 0, tap, version: 1 });
    readers.set(signature, reader);
  }
  const time = values.time ? Date.parse(values.time) : 1790294400000;
  return reader.read([
    0,
    identity(time),
    ...names.flatMap((name, i) => [i + 1, values[name]]),
  ]);
}

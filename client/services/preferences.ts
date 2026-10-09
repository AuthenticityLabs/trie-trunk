import { transact } from "./database.ts";

export type Theme = "light" | "dark";

export async function loadTheme(): Promise<Theme> {
  const value: unknown = await transact("preferences", "readonly", (store) =>
    store.get("theme"),
  );
  return value === "dark" ? "dark" : "light";
}

export async function saveTheme(value: Theme): Promise<void> {
  await transact("preferences", "readwrite", (store) =>
    store.put(value, "theme"),
  );
}

export const pageSizes = [25, 50, 100, 250, 500];
export const defaultPageSize = 100;

export async function loadPageSize(): Promise<number> {
  const value: unknown = await transact("preferences", "readonly", (store) =>
    store.get("events_per_page"),
  );
  return typeof value === "number" && pageSizes.includes(value)
    ? value
    : defaultPageSize;
}

export async function savePageSize(value: number): Promise<void> {
  if (!pageSizes.includes(value))
    throw new Error("Unsupported events per page.");
  await transact("preferences", "readwrite", (store) =>
    store.put(value, "events_per_page"),
  );
}

import { transact } from "./database.ts";

export type Theme = "trunk" | "light" | "dark";
export const defaultTheme: Theme = "trunk";
export const themeNames: Record<Theme, string> = {
  trunk: "Trie-Trunk", light: "Light", dark: "Dark",
};
export const themeColors: Record<Theme, string> = {
  trunk: "#fafafa", light: "#f8f7f5", dark: "#1b211e",
};
export function nextTheme(theme: Theme): Theme {
  return theme === "trunk" ? "light" : theme === "light" ? "dark" : "trunk";
}

export async function loadTheme(): Promise<Theme> {
  const value: unknown = await transact("preferences", "readonly", (store) =>
    store.get("theme"),
  );
  return value === "light" || value === "dark" || value === "trunk"
    ? value : defaultTheme;
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

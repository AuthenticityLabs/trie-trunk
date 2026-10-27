import { transact } from "./database.ts";

export type Theme = "trunk" | "light" | "dark";
export type ThemePreference = "light" | "dark" | "auto";
export const defaultTheme: ThemePreference = "auto";
export const themeNames: Record<ThemePreference, string> = {
  light: "Light", dark: "Dark", auto: "Auto",
};
export const themeColors: Record<Theme, string> = {
  trunk: "#181818", light: "#f8f7f5", dark: "#1b211e",
};
export function nextTheme(theme: ThemePreference): ThemePreference {
  return theme === "light" ? "dark" : theme === "dark" ? "auto" : "light";
}
export function resolveTheme(preference: ThemePreference, systemDark: boolean): Theme {
  return preference === "light" || preference === "auto" && !systemDark ? "light" : "trunk";
}

export async function loadTheme(): Promise<ThemePreference> {
  const value: unknown = await transact("preferences", "readonly", (store) =>
    store.get("theme"),
  );
  return value === "light" || value === "dark" || value === "auto"
    ? value : defaultTheme;
}

export async function saveTheme(value: ThemePreference): Promise<void> {
  await transact("preferences", "readwrite", (store) =>
    store.put(value, "theme"),
  );
}

export const pageSizes = [25, 50, 100, 250, 500];
export const defaultPageSize = 100;

export async function loadPageSize(): Promise<number> {
  try { const n=Number(sessionStorage.getItem('snoot:search:page-size'));return pageSizes.includes(n)?n:defaultPageSize; }
  catch { return defaultPageSize; }
}
export async function savePageSize(value: number): Promise<void> {
  if (!pageSizes.includes(value)) throw Error('Unsupported events per page.');
  sessionStorage.setItem('snoot:search:page-size',String(value));
}

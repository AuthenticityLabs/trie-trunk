import { For, Show, type JSX } from "solid-js";
import type { Theme } from "../services/preferences";
import { Icon, Mark, type IconName } from "./icon";
import portrait from "../assets/snoot-portrait.png";
import forward from "../assets/snoot-forward.png";

export type Page = "Search" | "Dashboard" | "Alerts";
const navigation: { title: Page; icon: IconName }[] = [
  { title: "Search", icon: "search" },
  { title: "Dashboard", icon: "dashboard" },
  { title: "Alerts", icon: "bell" },
];

export function Navigation(props: {
  theme: Theme;
  themeBusy: boolean;
  themeError: string;
  onToggleTheme: () => void;
  page: Page;
  count: number;
  hasToken: boolean;
  onNavigate: (page: Page) => void;
  onHelp: () => void;
  onConnection: () => void;
  children?: JSX.Element;
}) {
  return (
    <aside class="sidebar" aria-label="Main navigation">
      <button
        type="button"
        class="brand"
        onClick={props.onToggleTheme}
        disabled={props.themeBusy}
        aria-label="Dark mode"
        aria-pressed={props.theme === "dark"}
        title={`Switch to ${props.theme === "dark" ? "light" : "dark"} mode`}
      >
        <img
          class="brand-portrait"
          src={props.theme === "dark" ? forward : portrait}
          alt=""
          width="1254"
          height="1254"
        />
      </button>
      <Show when={props.themeError}>
        <p class="theme-error" role="alert">{props.themeError}</p>
      </Show>
      <div class="workspace-label">
        <span class="workspace-dot" />
        <span>Local workspace</span>
      </div>
      <div class="nav-label">EXPLORE</div>
      <nav>
        <For each={navigation}>
          {(item) => (
            <button
              class="nav-item"
              classList={{ active: props.page === item.title }}
              aria-current={props.page === item.title ? "page" : undefined}
              title={item.title}
              onClick={() => props.onNavigate(item.title)}
            >
              <Icon name={item.icon} />
              <span>{item.title}</span>
              <Show when={item.title === "Search" && props.count}>
                <small>{props.count.toLocaleString()}</small>
              </Show>
            </button>
          )}
        </For>
      </nav>
      <Show
        when={props.page === "Search"}
        fallback={
          <div class="sidebar-note">
            <div class="leaf-tile">
              <Icon name="leaf" size={22} />
            </div>
            <strong>
              Less noise.
              <br />
              More signal.
            </strong>
            <p>Follow the story in your events.</p>
            <span>snoot</span>
          </div>
        }
      >
        {props.children}
      </Show>
      <div class="sidebar-bottom">
        <button
          class="nav-item"
          onClick={() => props.onHelp()}
          title="Search guide"
        >
          <Icon name="help" />
          <span>Search guide</span>
        </button>
        <button
          class="nav-item"
          onClick={() => props.onConnection()}
          title="Connection"
        >
          <Icon name="settings" />
          <span>Connection</span>
          <span
            class={`connection-dot ${props.hasToken ? "configured" : ""}`}
          />
        </button>
        <div class="sidebar-footer">
          <span class="avatar">
            <Mark size={24} />
          </span>
          <div>
            <strong>Your workspace</strong>
            <small>Built for the details</small>
          </div>
        </div>
      </div>
    </aside>
  );
}

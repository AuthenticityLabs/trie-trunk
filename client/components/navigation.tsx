import { For, Show, type JSX } from "solid-js";
import { nextTheme, themeNames, type ThemePreference } from "../services/preferences";
import { Icon, ProfileMark, type IconName } from "./icon";
import forward from "../assets/snoot-forward.png";

export type Page = "Search" | "Dashboards" | "Alerts" | "Collections" | "Configs";
const navigation: { title: Page; icon: IconName }[] = [
  { title: "Search", icon: "search" },
  { title: "Dashboards", icon: "dashboard" },
  { title: "Alerts", icon: "bell" },
  { title: "Collections", icon: "clock" },
  { title: "Configs", icon: "settings" },
];

export function Navigation(props: {
  theme: ThemePreference;
  themeBusy: boolean;
  themeError: string;
  onToggleTheme: () => void;
  page: Page;
  count: number;
  hasToken: boolean;
  onNavigate: (page: Page) => void;
  onHelp: () => void;
  onConnection: () => void;
  onProfile: () => void;
  onLogout: () => void;
  children?: JSX.Element;
}) {
  return (
    <aside class="sidebar" aria-label="Main Navigation">
      <button
        type="button"
        class="brand"
        onClick={props.onToggleTheme}
        disabled={props.themeBusy}
        aria-label={`${themeNames[props.theme]} Theme. Switch to ${themeNames[nextTheme(props.theme)]} Theme`}
        title={`Switch to ${themeNames[nextTheme(props.theme)]} Theme`}
      >
        <img
          class="brand-portrait"
          src={forward}
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
        <span>Local Workspace</span>
      </div>
      <hr class="nav-divider" />
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
          title="Search Guide"
        >
          <Icon name="help" />
          <span>Search Guide</span>
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
        <div class="sidebar-account" aria-label="Demo User and Session">
          <button class="nav-item account-profile" onClick={props.onProfile} title="Demo User" aria-label="Demo User">
            <span class="avatar"><ProfileMark size={36}/></span><span>Demo User</span>
          </button>
          <button class="nav-item" onClick={props.onLogout} title="Log Out" aria-label="Log Out">
            <Icon name="logout"/><span>Log Out</span>
          </button>
        </div>
      </div>
    </aside>
  );
}

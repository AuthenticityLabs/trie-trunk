import snoot from "../assets/snoot-portrait.png";

import paths from "../assets/icons.json";

export type IconName = keyof typeof paths;

export function Icon(props: { name: IconName; size?: number; class?: string }) {
  return (
    <svg
      class={`icon ${props.class || ""}`}
      width={props.size || 18}
      height={props.size || 18}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <path d={paths[props.name]} />
    </svg>
  );
}

export function ProfileMark(props: { size?: number }) {
  return (
    <img
      class="snoot-mark"
      src={snoot}
      alt=""
      width={props.size || 30}
      height={props.size || 30}
      aria-hidden="true"
    />
  );
}

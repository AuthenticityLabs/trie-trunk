import { createEffect, onCleanup, untrack } from "solid-js";

/** Observe the end inside the actual scrolling container, including short lists. */
export function ScrollEnd(props: {
  root?: () => HTMLElement | undefined;
  enabled: boolean;
  version: number;
  onNearEnd: () => void;
}) {
  let marker!: HTMLDivElement;
  createEffect(() => {
    props.version;
    if (!props.enabled) return;
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) {
        observer.disconnect();
        untrack(props.onNearEnd);
      }
    }, { root: props.root?.() ?? null, rootMargin: "0px 0px 400px 0px" });
    observer.observe(marker);
    onCleanup(() => observer.disconnect());
  });
  return <div ref={marker} class="scroll-end" aria-hidden="true" />;
}

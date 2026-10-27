import {onCleanup, onMount} from 'solid-js';

export default function RetentionSignal(props: {basis: string}) {
  let details!: HTMLDetailsElement;
  onMount(() => {
    const closeOutside = (event: PointerEvent) => {
      if (!details.contains(event.target as Node)) details.open = false;
    };
    document.addEventListener('pointerdown', closeOutside);
    onCleanup(() => document.removeEventListener('pointerdown', closeOutside));
  });
  return <details class="quota-forecast-signal" ref={details} onKeyDown={event => {
    if (event.key === 'Escape') {details.open = false; details.querySelector('summary')?.focus();}
  }}>
    <summary title={props.basis}><span aria-hidden="true">ⓘ</span> Beyond Forecast Range</summary>
    <div class="quota-forecast-basis">{props.basis}</div>
  </details>;
}

export type CaretBox = { left: number; top: number; bottom: number };
export type VisibleBox = { left: number; top: number; width: number; height: number };

/** Keep suggestions usable when a browser cannot measure a collapsed text range. */
export function visibleQueryAnchor(caret: CaretBox, editor: VisibleBox, viewport: VisibleBox, belowEditor = false): CaretBox | undefined {
  const top = Math.max(editor.top, viewport.top), bottom = Math.min(editor.top + editor.height, viewport.top + viewport.height);
  const left = Math.max(editor.left, viewport.left), right = Math.min(editor.left + editor.width, viewport.left + viewport.width);
  if (bottom <= top || right <= left) return;
  const measured = Number.isFinite(caret.top) && Number.isFinite(caret.bottom) && caret.bottom > caret.top && caret.bottom > top && caret.top < bottom;
  return {
    left: Number.isFinite(caret.left) ? Math.max(left, Math.min(caret.left, right)) : left,
    top: belowEditor || !measured ? editor.top + editor.height - 24 : Math.max(top, caret.top),
    bottom: belowEditor || !measured ? editor.top + editor.height : Math.min(bottom, caret.bottom),
  };
}

/** Suggestions never flip above their anchor or cover the text being edited. */
export function placeQueryFlyover(caret: CaretBox, viewport: VisibleBox, desiredHeight: number) {
  const margin = 8, gap = 6;
  const left = viewport.left + margin, right = viewport.left + viewport.width - margin;
  const bottom = viewport.top + viewport.height - margin;
  const width = Math.max(0, Math.min(320, right - left));
  const below = Math.max(0, bottom - caret.bottom - gap);
  const height = Math.min(desiredHeight, below);
  return {
    left: Math.max(left, Math.min(caret.left, right - width)),
    top: caret.bottom + gap,
    width,
    height,
  };
}

/** Make room below a mobile editor without scrolling its top out of view. */
export function queryFlyoverScroll(editor: VisibleBox, viewport: VisibleBox, desiredHeight: number): number {
  const available = viewport.top + viewport.height - 8 - (editor.top + editor.height + 6);
  const shift = Math.max(0, editor.top - viewport.top - 8);
  return Math.max(0, Math.min(shift, desiredHeight - available));
}

// A textarea does not expose a caret DOM Range. Mirror its text layout so wrapped
// lines, tabs, proportional fonts, and internal scrolling retain the same anchor.
export function measureQueryCaret(input: HTMLTextAreaElement, mirror: HTMLDivElement): CaretBox {
  const style = getComputedStyle(input), box = input.getBoundingClientRect();
  for (const property of [
    'font-family', 'font-size', 'font-weight', 'font-style', 'font-variant',
    'line-height', 'letter-spacing', 'word-spacing', 'text-indent', 'text-align',
    'text-transform', 'direction', 'tab-size', 'padding-top', 'padding-right',
    'padding-bottom', 'padding-left', 'border-top-width', 'border-right-width',
    'border-bottom-width', 'border-left-width',
  ]) mirror.style.setProperty(property, style.getPropertyValue(property));
  Object.assign(mirror.style, {
    position: 'absolute', visibility: 'hidden', pointerEvents: 'none',
    boxSizing: 'border-box', borderStyle: 'solid', whiteSpace: 'pre-wrap',
    overflowWrap: 'break-word', overflow: 'hidden',
    // Document coordinates avoid fixed-position offsets while mobile browsers pan
    // the visual viewport to keep a focused textarea above the keyboard.
    left: `${box.left + window.scrollX}px`, top: `${box.top + window.scrollY}px`,
    width: `${input.clientWidth + parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth)}px`,
  });
  const position = input.selectionStart;
  const marker = document.createElement('span');
  marker.textContent = input.value.slice(position) || '|';
  mirror.replaceChildren(document.createTextNode(input.value.slice(0, position)), marker);
  const range = document.createRange();
  range.setStart(marker.firstChild!, 0);
  range.setEnd(marker.firstChild!, [...marker.textContent][0].length);
  const rect = range.getClientRects()[0] ?? marker.getBoundingClientRect();
  const lineHeight = parseFloat(style.lineHeight) || parseFloat(style.fontSize) * 1.8;
  const top = rect.top - input.scrollTop - Math.max(0, lineHeight - rect.height) / 2;
  return { left: rect.left - input.scrollLeft, top, bottom: top + lineHeight };
}

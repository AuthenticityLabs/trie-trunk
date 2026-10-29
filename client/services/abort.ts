/** Scoped cancellation for windows and workers, including Ventura's WebKit.
 * Dispose after completion so long-lived parent signals retain no listeners. */
export function createAbortScope(signals: readonly AbortSignal[] = [], timeoutMs?: number) {
  if (timeoutMs !== undefined && (!Number.isInteger(timeoutMs) || timeoutMs < 0 || timeoutMs > 0x7fffffff)) {
    throw new RangeError('Invalid cancellation timeout');
  }
  const controller = new AbortController();
  const listeners = new Map<AbortSignal, () => void>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  function dispose() {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
    for (const [signal, listener] of listeners) signal.removeEventListener('abort', listener);
    listeners.clear();
  }
  function abort(reason?: unknown) {
    dispose();
    controller.abort(reason);
  }
  for (const signal of new Set(signals)) {
    if (signal.aborted) {
      abort(signal.reason);
      break;
    }
    const listener = () => abort(signal.reason);
    listeners.set(signal, listener);
    signal.addEventListener('abort', listener, { once: true });
  }
  if (!controller.signal.aborted && timeoutMs !== undefined) {
    timer = setTimeout(() => abort(new DOMException('The request timed out', 'TimeoutError')), timeoutMs);
  }
  return { signal: controller.signal, abort, dispose };
}

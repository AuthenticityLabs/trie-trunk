/** Keep the failed operation when WebKit supplies only a generic DOMException. */
export function storageError(operation: string, error: unknown): Error {
  if (error instanceof Error && error.name === 'LocalStorageError') return error;
  // Callers use NotFoundError to distinguish an absent optional file.
  if (error instanceof DOMException && error.name === 'NotFoundError') return error;
  const detail = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
  const failure = new Error(`${operation} — ${detail}`, { cause: error });
  failure.name = 'LocalStorageError';
  return failure;
}

export async function storageStep<T>(operation: string, work: () => Promise<T>): Promise<T> {
  try { return await work(); }
  catch (error) { throw storageError(operation, error); }
}

export function localStorageRoot() {
  return storageStep('Open Local Storage', () => navigator.storage.getDirectory());
}

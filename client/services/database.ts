type Store = "preferences";

export function transact<T>(
  name: Store | Store[],
  mode: IDBTransactionMode,
  action: (store: IDBObjectStore, transaction: IDBTransaction) => IDBRequest<T>,
): Promise<T> {
  return new Promise((resolve, reject) => {
    let blocked = false;
    const open = indexedDB.open("snoot-profile", 1);
    open.onupgradeneeded = () => {
      const db = open.result;
      if (!db.objectStoreNames.contains("preferences"))
        db.createObjectStore("preferences");
    };
    open.onerror = () => reject(open.error);
    open.onblocked = () => {
      blocked = true;
      reject(
        new Error(
          "Close other snoot tabs and retry to update browser storage.",
        ),
      );
    };
    open.onsuccess = () => {
      const db = open.result;
      if (blocked) {
        db.close();
        return;
      }
      db.onversionchange = () => db.close();
      let transaction: IDBTransaction | undefined;
      try {
        transaction = db.transaction(name, mode);
        const request = action(
          transaction.objectStore(Array.isArray(name) ? name[0] : name),
          transaction,
        );
        transaction.oncomplete = () => {
          db.close();
          resolve(request.result);
        };
        transaction.onabort = () => {
          db.close();
          reject(
            transaction!.error ??
              request.error ??
              new Error("Browser storage request failed."),
          );
        };
      } catch (cause) {
        transaction?.abort();
        db.close();
        reject(cause);
      }
    };
  });
}

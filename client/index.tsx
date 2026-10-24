import { ErrorBoundary } from "solid-js";
import { render } from "solid-js/web";
import App from "./app";
import "./style.css";

render(
  () => (
    <ErrorBoundary
      fallback={(cause) => (
        <main class="startup-error" role="alert">
          <h1>Snoot Couldn’t Start</h1>
          <p>{cause instanceof Error ? cause.message : String(cause)}</p>
          <button class="button" onClick={() => location.reload()}>
            Reload
          </button>
        </main>
      )}
    >
      <App />
    </ErrorBoundary>
  ),
  document.getElementById("root")!,
);

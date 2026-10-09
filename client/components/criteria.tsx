import { For } from "solid-js";
import {
  parameterNames,
  type Choices,
  type Parameters,
} from "../services/search";

export function Criteria(props: {
  scope: "Collect" | "Search";
  parameters: Parameters;
  onParameters: (value: Parameters) => void;
  choices: Choices;
}) {
  function options(name: (typeof parameterNames)[number]) {
    const values = props.choices[name] ?? [];
    const selected = props.parameters[name];
    return selected && !values.includes(selected)
      ? [...values, selected].sort()
      : values;
  }

  return (
    <div class="field-controls">
      <For each={parameterNames}>
        {(name) => (
          <label class="control">
            <span>{name[0].toUpperCase() + name.slice(1)}</span>
            <select
              aria-label={`${props.scope} ${name}`}
              value={props.parameters[name] ?? ""}
              onChange={(event) =>
                props.onParameters({
                  ...props.parameters,
                  [name]: event.currentTarget.value,
                })
              }
            >
              <option value="">All</option>
              <For each={options(name)}>
                {(value) => <option value={value}>{value}</option>}
              </For>
            </select>
          </label>
        )}
      </For>
    </div>
  );
}

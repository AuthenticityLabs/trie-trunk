import paths from '../../client/assets/icons.json' with {type: 'json'};

// The website and Snoot use the same UI icon geometry.
export function icon(name) {
 return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${paths[name]}"/></svg>`;
}

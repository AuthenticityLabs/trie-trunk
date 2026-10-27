import * as pil from 'pill-packer/compression';
import {resourceBinding, type PILDefinition} from './layout.ts';
export {mergeFrames, compressionFeature} from 'pill-packer/compression';
export type {Inflater} from 'pill-packer/compression';

const profiles = new WeakMap<PILDefinition, pil.IdentityProfile>();
function profile(def: PILDefinition): pil.IdentityProfile {
  let value = profiles.get(def);
  if (!value) {
    const roles = resourceBinding(def)?.roles;
    const fields = roles ? [roles.event_id, roles.id, roles.revision_id]
      .filter((name): name is string => !!name)
      .map(name => def.fields.findIndex(field => field.name === name)) : [];
    value = {primary: fields[0] ?? -1, fields, validate: validateEventIdentity};
    profiles.set(def, value);
  }
  return value;
}
function validateEventIdentity(id: Uint8Array): void {
  if (id.length !== 16 || id[6] >> 4 !== 7 || id[8] >> 6 !== 2)
    throw Error('Invalid compressed event identity');
}
export function compressedMarker(raw: Uint8Array, def: PILDefinition): boolean {
  return pil.compressedMarker(raw, def, profile(def));
}
export function envelopeLength(raw: Uint8Array, def: PILDefinition): number | undefined {
  return pil.envelopeLength(raw, def, profile(def));
}
export function expandEvent(raw: Uint8Array, def: PILDefinition,
  validate?: (bytes: Uint8Array) => void, inflate?: pil.Inflater): Promise<Uint8Array> {
  return pil.expandEvent(raw, def, profile(def), validate, inflate);
}

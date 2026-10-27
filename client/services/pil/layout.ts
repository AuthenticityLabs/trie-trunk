export {textDecoder,width,extended,span,integer,read,scalar} from 'pill-packer/layout';
export type {PILField,Span} from 'pill-packer/layout';
import type {PILDefinition as PortableDefinition} from 'pill-packer/layout';
export type ResourceBinding = {
  service?: string; resource?: string;
  roles: {event_id:string; id?:string; revision_id?:string; shared?:string; duration?:string};
  path?: string[];
};
export type PILDefinition=PortableDefinition & {resource?:ResourceBinding};
export function resourceBinding(def:Pick<PILDefinition,'resource'>):ResourceBinding|undefined{return def.resource}

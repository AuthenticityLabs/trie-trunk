// Application headers are interpreted here, outside the portable PAC parser.
export * from 'pill-packer/pac';
import {pacHeader as readHeader, type PACHeader as PortableHeader} from 'pill-packer/pac';
export type PACHeader = PortableHeader & {source?:string};
export function pacHeader(...args:Parameters<typeof readHeader>):PACHeader|undefined {
 const header=readHeader(...args);if(!header)return undefined;
 const source=header.extensions.find(e=>e.type===1);
 if(source && source.version!==0)throw Error('Unsupported source binding');
 return {...header,source:source?new TextDecoder('utf-8',{fatal:true}).decode(source.bytes):undefined};
}

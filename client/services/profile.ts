import {validTimeZone} from './time-zone.ts';
import {transact} from './database.ts';
import {loadTheme,loadPageSize,defaultTheme,pageSizes,defaultPageSize,type ThemePreference} from './preferences.ts';
import {localConnection,normalizeConnection,type ServerConnection} from './connections.ts';
export type Palette='trunk'|'snoot';
export type UserProfile={name:string;timeZone:string;appearance:ThemePreference;palette:Palette;pageSize:number;connections:ServerConnection[]};
export const defaultProfile=():UserProfile=>({name:'Demo User',timeZone:'UTC',appearance:defaultTheme,palette:'trunk',pageSize:defaultPageSize,connections:[localConnection()]});
export function normalizeProfile(value:UserProfile):UserProfile {
 if(!value||typeof value.name!=='string'||!value.name.trim()||value.name.length>128||!['light','dark','auto'].includes(value.appearance)||!['trunk','snoot'].includes(value.palette)||!pageSizes.includes(value.pageSize)||!Array.isArray(value.connections))throw Error('Enter a name and valid profile preferences.');
 const connections=value.connections.map(normalizeConnection),ids=new Set(connections.map(c=>c.id));
 if(ids.size!==connections.length)throw Error('Connection IDs must be unique.');
 return {name:value.name.trim(),timeZone:validTimeZone(value.timeZone??'UTC'),appearance:value.appearance,palette:value.palette,pageSize:value.pageSize,connections};
}
export async function loadProfile():Promise<UserProfile>{
 const value:unknown=await transact('preferences','readonly',store=>store.get('profile'));
 if(value!==undefined)return normalizeProfile(value as UserProfile);
 return {...defaultProfile(),appearance:await loadTheme(),pageSize:await loadPageSize()};
}
export async function saveProfile(value:UserProfile):Promise<UserProfile>{
 const profile=normalizeProfile(value);
 await transact('preferences','readwrite',store=>store.put(profile,'profile'));
 return profile;
}

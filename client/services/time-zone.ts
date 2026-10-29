const formatters=new Map<string,Intl.DateTimeFormat>();
export function validTimeZone(zone:string):string{
 if(typeof zone!=='string'||!zone)throw Error('Choose a Time Zone.');
 try{return new Intl.DateTimeFormat('en',{timeZone:zone}).resolvedOptions().timeZone;}catch{throw Error('Choose a valid IANA Time Zone.');}
}
function formatter(zone:string){let f=formatters.get(zone);if(!f){f=new Intl.DateTimeFormat('en-CA',{timeZone:zone,year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',fractionalSecondDigits:3,hourCycle:'h23'});formatters.set(zone,f)}return f;}
export function zonedTimestamp(ms:number,zone='UTC'):string{
 const p=Object.fromEntries(formatter(zone).formatToParts(ms).map(p=>[p.type,p.value]));return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}:${p.second}.${p.fractionalSecond}`;
}
/** A numeric offset is resolved per timestamp, never cached across DST changes. */
export function timeZoneOffset(ms:number,zone='UTC'):number{return (Date.parse(zonedTimestamp(ms,zone)+'Z')-ms)/60000;}
export function parseZonedTimestamp(text:string,zone='UTC'):number{
 const wall=Date.parse(text+'Z');if(!Number.isFinite(wall))throw Error('Enter a valid timestamp.');
 const canonical=new Date(wall).toISOString().slice(0,-1),offsets=new Set<number>();
 for(let h=-48;h<=48;h+=6)offsets.add(timeZoneOffset(wall+h*3600000,zone));
 const matches=[...offsets].map(offset=>wall-offset*60000).filter(ms=>zonedTimestamp(ms,zone)===canonical);
 if(matches.length===0)throw Error('That local time does not exist because the clock changes. Choose another time.');
 if(matches.length>1)throw Error('That local time occurs twice because the clock changes. Select UTC in Preferences to choose the exact instant.');
 return matches[0];
}
export function utcInput(ms:number):string{return new Date(ms).toISOString().slice(0,-1);}

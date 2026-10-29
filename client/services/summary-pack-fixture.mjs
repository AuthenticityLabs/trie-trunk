export function summaryArray(packs) {
 const ext=n=>{if(n<65535){const b=Buffer.alloc(2);b.writeUInt16LE(n);return b}const b=Buffer.alloc(6);b.writeUInt16LE(65535);b.writeUInt32LE(n,2);return b};
 let end=0;const array=Buffer.concat([ext(packs.length),...packs.map(p=>ext(end+=p.length))]);
 return Buffer.concat([Buffer.from([0xc0]),ext(array.length+end),array,...packs]);
}
export function summaryResponse(packs) {return new Response(summaryArray(packs),{headers:{'Content-Type':'application/pil'}})}

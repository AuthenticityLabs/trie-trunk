export function summaryArray(packs) {
  const head=(major,n)=>n<24?Buffer.from([major<<5|n]):n<=255?Buffer.from([major<<5|24,n]):n<=65535?Buffer.from([major<<5|25,n>>8,n&255]):Buffer.from([major<<5|26,n>>>24,n>>>16&255,n>>>8&255,n&255]);
  return Buffer.concat([head(4,packs.length),...packs.flatMap(p=>[head(2,p.length),p])]);
}
export function summaryResponse(packs) {
  return new Response(summaryArray(packs),{headers:{'Content-Type':'application/cbor'}});
}

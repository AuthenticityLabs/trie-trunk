/** Event time and row addressing belong to Seed, not to the portable PIL codec. */
const EPOCH_LOW_RANGE=0x100000000;
/** Internal sortable key for existing event interfaces; not the incoming UUID. */
export function seedKey(record:number,time:number):Uint8Array{
 if(!Number.isInteger(record)||record<0||record>=2**24||!Number.isSafeInteger(time)||time<0||time>=2**48)throw Error('Invalid Seed identity');
 const key=new Uint8Array(16);let n=time;
 for(let i=5;i>=0;i--){key[i]=n%256;n=Math.floor(n/256)}
 key[6]=0x70;key[8]=0x80;key[13]=record>>>16;key[14]=record>>>8;key[15]=record;return key;
}
/** The hour's high prefix plus its known range resolves a low-word rollover. */
export function timeFromLowWord(low:number,hour:number):number{
 if(!Number.isInteger(low)||low<0||low>=EPOCH_LOW_RANGE||!Number.isSafeInteger(hour)||hour<0||hour>=2**48)throw Error('Invalid index timestamp');
 let time=Math.floor(hour/EPOCH_LOW_RANGE)*EPOCH_LOW_RANGE+low;
 if(time<hour)time+=EPOCH_LOW_RANGE;
 if(time>=hour+3600000||time>=2**48)throw Error('Index timestamp outside hour');return time;
}
export function stateAddress(taxonomy:number,state:number):string{
 if(!Number.isInteger(taxonomy)||taxonomy<0||taxonomy>65535||!Number.isInteger(state)||state<0||state>65535)throw Error('Invalid state address');
 return `0x${taxonomy.toString(16).padStart(4,'0')}${state.toString(16).padStart(4,'0')}`;
}

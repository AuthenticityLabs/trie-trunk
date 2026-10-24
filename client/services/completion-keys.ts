/** Text cursor movement wins unless a completion menu owns the key. */
export function completionKey(input:{key:string;shift:boolean;modified:boolean;composing:boolean;open:boolean;count:number;active:number;collapsed:boolean;atEnd:boolean;engaged?:boolean}):{kind:'open'|'move'|'accept';index:number}|undefined {
 const {key,shift,modified,composing,open,count,active,collapsed,atEnd}=input;
 if(composing||modified||shift||!collapsed||!count)return;
 if(key==='ArrowRight'&&(atEnd||open&&active>=0))return {kind:open&&(active>=0||input.engaged)?'accept':'open',index:active>=0&&active<count?active:0};
 if(key==='ArrowDown'||key==='ArrowUp'){
  const index=active<0?(key==='ArrowDown'?0:count-1):(active+(key==='ArrowDown'?1:-1)+count)%count;
  return {kind:'move',index};
 }
 if(!open)return;
 if(key==='Tab'||key==='Enter'&&active>=0)return {kind:'accept',index:active>=0&&active<count?active:0};
}

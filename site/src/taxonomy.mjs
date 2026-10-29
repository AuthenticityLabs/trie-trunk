const tiers = [
 ['Trunk', 'The platform contains your caches.'],
 ['Caches', 'Separate working spaces, such as prod, test, and sand. Each has its own hollow in the trunk.'],
 ['Services', 'Applications and services branch out from a cache.'],
 ['Resources', 'What a service exposes or observes. A collection is one kind of resource.'],
 ['Level', 'Classify events as info, error, debug, or trace.'],
 ['Access', 'Read and write sit beneath the level. Writes can carry collection changes for replay.'],
];

// Schematic completed-sale counts by sales completion time, independent of fulfillment updates.
const orderTrend = [
 {hour:'09:00', disconnect:2, add:48},
 {hour:'10:00', disconnect:3, add:44},
 {hour:'11:00', disconnect:5, add:38},
 {hour:'12:00', disconnect:9, add:30},
 {hour:'13:00', disconnect:15, add:22},
 {hour:'14:00', disconnect:24, add:16},
];

// The wood is illustrative; the overlaid paths express the actual hierarchy.
// Keep labels as SVG text so the diagram scales cleanly in either color theme.
const tree = `<svg class="taxonomy-tree" viewBox="0 0 420 650" role="img" aria-labelledby="taxonomy-tree-title taxonomy-tree-desc">
 <title id="taxonomy-tree-title">The Trie-Trunk Taxonomy</title>
 <desc id="taxonomy-tree-desc">Caches are hollows within the trunk. The prod cache branches into payment and identity services. Payment branches into transactions and refunds resources. Identity branches into permissions. Transactions branches into info and error levels. The info level branches into read and write access. The test cache has its own identity service. The expanded path is Trunk, prod, payment, transactions, info, write.</desc>
 <path class="tree-ground" d="M45 618H375"/>
 <path class="tree-wood" d="M105 615C143 589 154 558 153 509L153 418C151 378 120 373 94 340L82 315C113 330 139 355 160 360C171 327 175 305 174 281C192 304 195 327 195 348C225 332 268 323 312 303C295 338 254 350 225 372C216 405 218 446 237 463C264 469 302 454 337 432C318 468 284 487 251 491C246 539 258 580 299 615Z"/>
 <g class="tree-grain">
  <path d="M133 598C168 563 166 538 166 494M279 599C236 561 230 528 231 501M180 398C183 377 193 364 217 351M205 578C185 558 179 538 183 514"/>
  <path d="M168 434C143 451 147 486 171 497C204 512 224 480 213 450C204 427 183 421 168 434Z"/>
  <path d="M205 521C184 525 179 546 190 562C207 582 230 564 230 545C228 530 219 522 205 521Z"/>
 </g>
 <g class="tree-limbs">
  <path d="M180 447C181 388 129 391 97 335M180 447C180 379 243 351 315 335M97 335C74 304 80 270 80 235M97 335C126 306 159 297 173 268M315 335C331 300 326 272 326 239M80 235C54 212 52 177 52 141M80 235C120 216 148 197 151 163M52 141C35 119 30 85 34 57M52 141C77 116 102 89 106 57M211 548C260 516 322 483 350 446"/>
 </g>
 <g class="tree-leaves">
  <path d="M34 57C14 52 15 30 22 18C39 25 45 41 34 57ZM106 57C95 39 106 21 125 16C129 35 124 50 106 57Z"/>
 </g>
 <g class="tree-tier" data-tree-tier="0">
  <rect x="161" y="597" width="90" height="33" rx="3"/>
  <text x="206" y="619">Trunk</text>
 </g>
 <g class="tree-tier tree-hollow is-active" data-tree-tier="1">
  <circle cx="181" cy="464" r="27"/>
  <text x="181" y="469">prod</text>
  <circle cx="211" cy="548" r="23"/>
  <text x="211" y="553">test</text>
 </g>
 <g class="tree-tier" data-tree-tier="2">
  <rect x="41" y="321" width="112" height="32" rx="3"/><text x="97" y="342">payment</text>
  <rect x="267" y="321" width="96" height="32" rx="3"/><text x="315" y="342">identity</text>
  <rect x="302" y="430" width="96" height="32" rx="3"/><text x="350" y="451">identity</text>
 </g>
 <g class="tree-tier" data-tree-tier="3">
  <rect x="12" y="219" width="136" height="32" rx="3"/><text x="80" y="240">transactions</text>
  <rect x="128" y="252" width="90" height="32" rx="3"/><text x="173" y="273">refunds</text>
  <rect x="262" y="223" width="128" height="32" rx="3"/><text x="326" y="244">permissions</text>
 </g>
 <g class="tree-tier" data-tree-tier="4">
  <rect x="22" y="125" width="60" height="32" rx="3"/><text x="52" y="146">info</text>
  <rect x="116" y="147" width="70" height="32" rx="3"/><text x="151" y="168">error</text>
 </g>
 <g class="tree-tier" data-tree-tier="5">
  <rect x="5" y="53" width="58" height="32" rx="3"/><text x="34" y="74">read</text>
  <rect x="72" y="53" width="68" height="32" rx="3"/><text x="106" y="74">write</text>
 </g>
 <g class="tree-continuations"><path d="M151 147V112M173 252L191 218M326 223V189M350 430L365 400"/></g>
</svg>`;

export const taxonomyDiagram = `
 <div class="taxonomy-layout">
  <figure class="taxonomy-figure">${tree}<figcaption>Example paths · One cache expanded</figcaption></figure>
  <div class="taxonomy-guide">
   <p class="mono-label">Select a Tier to Highlight</p>
   <ol>${tiers.map(([name,copy],i)=>`<li><button type="button" data-taxonomy-tier="${i}" aria-pressed="${i===1}"><span class="taxonomy-number">0${i+1}</span><span><strong>${name}</strong><span>${copy}</span></span></button></li>`).join('')}</ol>
   <p class="taxonomy-mobile-copy" aria-live="polite">${tiers[1][1]}</p>
  </div>
 </div>`;
export const taxonomyBreadcrumb = `<code>Trunk <span>›</span> prod <span>›</span> payment <span>›</span> transactions <span>›</span> info <span>›</span> write</code>`;
export const taxonomyIllustration = `${taxonomyDiagram}
 <div class="taxonomy-path"><span class="mono-label">An Event’s Place</span>${taxonomyBreadcrumb}</div>`;

export const timeTrieContent = `
 <div class="trie-copy"><p>The time trie offers 5-byte event addressing, supporting as much as 16 million events per hour across a rolling 7-year depth of history. And that maximal sizing is viable for every unique storage branch.</p>
 <p>Reach an hour in O(1), then locate a time boundary within its time sorted event index via binary search in O(log n), where n is the number of events in that hour.</p></div>`;

export const stateTrieContent = `
 <div class="trie-copy"><p>The event state trie combines selected low-cardinality fields—those with relatively few distinct values—into shared field/value paths. Common prefixes are stored once, and a two-byte reference identifies a node and the values along its path. Events with the same combination reuse that reference, reducing repetition and providing compact signatures for analytics.</p>
 <p>The event object stays flat. For <code>product-orders</code>, the <code>product: "premiere"</code> node branches into <code>type: "new_service"</code> (<code>C3</code>) and <code>type: "termination"</code> (<code>C7</code>). Group completed orders by these signatures to see service cancellations rise as new-service sales fall.</p></div>
 <figure class="state-example" aria-label="Shared Product Paths, Flat Events, and Order Trends">
  <div class="state-sharing">
   <div class="state-path"><h3>Share Low-Cardinality Paths</h3><code>product-orders.product</code><ol><li><div><code>"premiere"</code></div><ol><li><div><code>type: "new_service"</code><span class="state-leaf state-leaf-add">C3</span></div></li><li><div><code>type: "termination"</code><span class="state-leaf">C7</span></div></li></ol></li></ol><p>The product prefix is shared by both business order types. This example includes completed sales, identified by <code>completed_at</code>. Repeated product/type combinations reuse the same path.</p></div>
  </div>
  <div class="state-reuse"><h3>Replace Shared Values With Their Reference</h3>
   <div class="state-flat-event state-flat-add"><h4>New Service Sold</h4><div class="state-transform">
    <div><span class="mono-label">Flat Event</span><pre><code>{
<span class="state-shared-fields">  "product": "premiere",
  "type": "new_service",</span>
  "completed_at": "2026-10-26T09:00:00Z"
}</code></pre></div>
    <span class="state-transform-arrow" aria-label="Shared values become C3">→</span>
    <div><span class="mono-label">State Reference + Event Fields</span><pre><code>{
  <span class="state-inline-ref">C3</span>,
  "completed_at": "2026-10-26T09:00:00Z"
}</code></pre></div>
   </div></div>
   <div class="state-flat-event"><h4>Service Cancellation Agreed</h4><div class="state-transform">
    <div><span class="mono-label">Flat Event</span><pre><code>{
<span class="state-shared-fields">  "product": "premiere",
  "type": "termination",</span>
  "completed_at": "2026-10-26T14:00:00Z"
}</code></pre></div>
    <span class="state-transform-arrow" aria-label="Shared values become C7">→</span>
    <div><span class="mono-label">State Reference + Event Fields</span><pre><code>{
  <span class="state-inline-ref">C7</span>,
  "completed_at": "2026-10-26T14:00:00Z"
}</code></pre></div>
   </div></div>
   <p>The shared <code>product</code> and <code>type</code> values become C3 or C7. Each seed keeps its own <code>completed_at</code> and other event-specific fields. The completed sale may already be fulfilled or scheduled for future fulfillment.</p>
  </div>
  <div class="state-trend"><div class="state-trend-heading"><h3>Service Cancellations Rise. New Sales Fall.</h3><span>Completed Orders per Hour</span></div>
   <div class="state-trend-key"><span><i></i>Service Cancellation · C7</span><span><i class="state-add"></i>New Service · C3</span></div>
   <div class="state-trend-bars" role="img" aria-label="Illustrative completed sales transaction counts for premiere, grouped by sales completion time. ${orderTrend.map(row=>`${row.hour}: ${row.disconnect} agreed service cancellations, ${row.add} new-service sales`).join('; ')}.">${orderTrend.map(row=>`<div><div class="state-bar-pair"><div><span>${row.disconnect}</span><i style="--state-count:${row.disconnect}" aria-hidden="true"></i></div><div><span>${row.add}</span><i class="state-add" style="--state-count:${row.add}" aria-hidden="true"></i></div></div><code>${row.hour}</code></div>`).join('')}</div>
   <p>Group by <code>completed_at</code>, when the sale or cancellation was finalized. Count each completed transaction once, including those scheduled for future fulfillment.</p>
  </div>
  <figcaption id="state-example-caption">Schematic state representations and illustrative counts. C3 and C7 are local to this resource’s bound trie. Two bytes encode the shared-value reference; total storage also includes the trie and each event’s remaining fields.</figcaption>
 </figure>`;

export const taxonomy = `<section class="wrap trie-explorer" id="tries">
 <div class="section-heading"><h2>Tries</h2><p>Explore the hierarchy, time indexing, and shared event states.</p></div>
 <div class="trie-tabs" role="tablist" aria-label="Trie Views">
  <button id="trie-tab-taxonomy" type="button" role="tab" data-trie-tab="taxonomy-panel" aria-controls="taxonomy-panel" aria-selected="true">Taxonomy</button>
  <button id="trie-tab-time" type="button" role="tab" data-trie-tab="time-trie" aria-controls="time-trie" aria-selected="false" tabindex="-1">Time</button>
  <button id="trie-tab-state" type="button" role="tab" data-trie-tab="event-state-trie" aria-controls="event-state-trie" aria-selected="false" tabindex="-1">State</button>
 </div>
<section class="taxonomy trie-panel" id="taxonomy-panel" role="tabpanel" aria-labelledby="trie-tab-taxonomy" tabindex="0">
 <div class="section-heading"><h3>Trie Trunk Taxonomy</h3><p>Caches in the trunk.<br>Services branch out.</p></div>
 ${taxonomyIllustration}
</section>
<section class="taxonomy trie-indexing trie-panel" id="time-trie" role="tabpanel" aria-labelledby="trie-tab-time" tabindex="0" hidden>
 <div class="section-heading"><h3>Time Trie Indexing</h3></div>
 ${timeTrieContent}
</section>
<section class="taxonomy trie-indexing trie-panel" id="event-state-trie" role="tabpanel" aria-labelledby="trie-tab-state" tabindex="0" hidden>
 <div class="section-heading"><h3>Event State Trie</h3></div>
 ${stateTrieContent}
</section>
</section>`;

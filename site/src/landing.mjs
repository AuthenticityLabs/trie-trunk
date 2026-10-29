import {icon} from './diagrams.mjs';
import {taxonomyDiagram,taxonomyBreadcrumb} from './taxonomy.mjs';

const arrow = icon('arrow');

const triePreview = `<div class="trie-workspace">
  <div class="taxonomy" id="taxonomy-view">
   <nav class="trie-breadcrumbs" aria-label="Taxonomy Path">${taxonomyBreadcrumb}</nav>
   <p class="taxonomy-original-intro">Caches in the trunk.<br>Services branch out.</p>
   ${taxonomyDiagram}
  </div>
</div>`;

const snootPreview = `<div class="snoot-slice"><iframe data-snoot-preview src="/snoot-preview/" title="Interactive Snoot Preview" loading="lazy" tabindex="0"></iframe></div>`;

export const landing = (lifecycle,brand) => `<div class="landing-pages">
 <section class="document-page" id="trunk" data-page-section="trunk" aria-labelledby="trunk-title"><div class="trunk-document wrap">
  <aside class="document-rail" aria-label="Trie-Trunk">
   <a class="brand main-brand" href="/" aria-label="Trie-Trunk Home">${brand}</a>
   <p>A time-series database.<br>Typed events.<br>Local analytics.</p>
   <a class="rail-launch" href="https://snoot.trietrunk.com">Open Snoot ${arrow}</a>
   <small>Open Source / Version Zero</small>
  </aside>
  <div class="document-body"><div class="trunk-intro"><h1 id="trunk-title"><span class="trunk-headline">Hierarchical Time-Series</span><span class="trunk-promise">Minimal Footprint<br>at Lightning Speeds.</span></h1><p>Typed events. Tiny indexes. Precomputed columnar analytics. All within a <a href="/hidden#storage">70% smaller storage footprint</a> than expanded JSONL in our million-event benchmark.</p></div><div class="homepage-chart-composition"><iframe data-snoot-preview src="/snoot-preview/?view=marketing" title="Interactive Sample Event Activity and P95 Duration Gauge"></iframe></div></div>
 </div></section>
 <section class="document-page" id="tries" data-page-section="trunk" aria-label="Trie Taxonomy"><div class="wrap trie-document">${triePreview}</div></section>
 <section class="snoot-page" id="snoot" data-page-section="snoot" aria-labelledby="snoot-title"><div class="hero-layout wrap">${snootPreview}<div class="hero-copy"><h2 id="snoot-title">Snoot through<br>Telemetry data.</h2><a class="button hero-action" href="https://snoot.trietrunk.com">Open Snoot ${arrow}</a></div></div><a class="snap-next" href="#seeds"><span>Seeds</span>${icon('down')}</a></section>
 <section class="document-page" id="seeds" data-page-section="seeds" aria-label="Lifecycle"><div class="wrap seeds-document">${lifecycle}</div><footer class="landing-footer wrap"><span>Authenticity Labs · Version Zero</span><div><a href="https://github.com/AuthenticityLabs/trie-trunk">GitHub ↗</a><a href="https://github.com/AuthenticityLabs/trie-trunk/blob/main/README.md#licensing">Licensing</a><a href="#trunk">Back to Trunk ↑</a></div></footer></section>
</div>`;

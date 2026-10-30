import {highlight} from './highlight.mjs';

export const layoutDefinition={pil:0,definition:'example/records',version:0,fields:[{name:'id',type:'U16'},{name:'count',type:'U32'},{name:'state',type:'STRING',values:['pending','authorized','canceled']},{name:'note',type:'STRING'}]};
export const layoutRecord={id:42,count:1500,state:'authorized',note:'OK'};
export const layoutHex='AB 2A 00 DC 05 00 00 01 02 00 4F 4B';
const escape=value=>String(value).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;');
const table=(heads,rows)=>`<div class="pil-table-wrap" tabindex="0"><table class="pil-table"><thead><tr>${heads.map(h=>`<th scope="col">${h}</th>`).join('')}</tr></thead><tbody>${rows.map(row=>`<tr>${row.map(cell=>`<td>${cell}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`;
const copyButton=(id,label)=>`<button class="code-copy" type="button" data-copy-code="${id}" aria-label="${label}"><svg class="copy-icon" viewBox="0 0 24 24" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg><svg class="copied-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12 4 4L19 6"/></svg><span data-copy-feedback role="status" aria-live="polite">Copy</span></button>`;
const codeBox=(id,title,code)=>`<div class="query-example"><div class="example-title"><span>${title}</span>${copyButton(id,`Copy ${title}`)}</div><pre><code id="${id}">${escape(code)}</code></pre></div>`;
export const definitionAgentPrompt = "task: Create definition.rx\npurpose: [describe the data]\nsamples: [sample file]\nspec: https://trietrunk.com/pill-packer.txt\ninstall: curl -fsSL https://trietrunk.com/install-rx.sh | sh\nschema: CDDL; root=record\nrules:\n  - Confirm types, ranges, byte limits, optional fields, and nulls.\n  - Samples are not constraints. Preserve extra fields unless specified.\n  - Dictionaries compress values; enums restrict them.\ncompile: rx write definition.cddl definition.rx --root record\ninspect: rx read definition.rx --pretty\ncheck: Sample round-trips and boundaries; flag unsupported CDDL.\ndeliver: definition.rx + brief rationale";
export const libraryExamples = [
  [
    "javascript",
    "JavaScript / WASM",
    "import { doc } from \"pill-packer\";\n\nconst rx = await doc.write(cddl.rx);\nlet pill = rx.fill(pill_bytes);\nlet pack = rx.pack(header_bytes);\n\n// Encode\npack.push(pill);\nconst cpack = pack.seal({ algorithm: \"gzip\", level: 1 });\n\n// Decode\npack = await cpack.unseal();\npill = pack.get(0);\nconst json = pill.json();\n\nrx.dispose();"
  ],
  [
    "rust",
    "Rust",
    "use pil_codec::api::{Doc, Fill, PackOptions, Compression};\n\nlet rx = Doc::write(cddl.rx)?;\nlet pill = rx.fill(Fill::Encoded(pill_bytes))?;\nlet mut pack = rx.pack(header_bytes, PackOptions::default())?;\n\n// Encode\npack.push(pill)?;\nlet cpack = pack.seal(Compression {\n    level: 1,\n    only_if_smaller: false,\n})?;\n\n// Decode\npack = cpack.unseal()?;\nlet pill = pack.get(0)?;\nlet json = pill.json()?;"
  ],
  [
    "go",
    "Go",
    "import pillpacker \"github.com/AuthenticityLabs/pill-packer/go/api\"\n\ndoc := pillpacker.Doc{}\nrx, err := doc.Write(cddl.rx)\nif err != nil { return nil, err }\ndefer rx.Close()\npill, err := rx.Fill(pill_bytes)\nif err != nil { return nil, err }\npack, err := rx.Pack(header_bytes, pillpacker.PackOptions{})\nif err != nil { return nil, err }\n\n// Encode\nif err = pack.Push(pill); err != nil { return nil, err }\ncpack, err := pack.Seal(pillpacker.Compression{Algorithm: \"gzip\", Level: 1})\nif err != nil { return nil, err }\n\n// Decode\npack, err = cpack.Unseal()\nif err != nil { return nil, err }\npill, err = pack.Get(0)\nif err != nil { return nil, err }\njson, err := pill.JSON()\nif err != nil { return nil, err }"
  ]
];

export const prescriptionOperations = [
  [
    "Doc",
    "doc.write(input[, format])",
    "Prepare a prescription. Raw .rx / PIL bytes need no format argument; JSON and CBOR bytes accept an explicit override."
  ],
  [
    "Doc",
    "doc.read(definitionBytes, recordBytes)",
    "Quick read; prepares a prescription and binds one encoded PIL record."
  ],
  [
    "Prescription",
    "rx.fill() / rx.fill(bytes) / rx.fill(fieldBuffers)",
    "Create an all-absent record, bind encoded PIL bytes, or assemble ordered field buffers. Definition constraints still apply."
  ],
  [
    "Prescription",
    "rx.pack(headerBytes, options)",
    "Create a mutable pack. Header bytes encode named application headers."
  ],
  [
    "Prescription",
    "rx.open(bytes) / rx.unpack(bytes)",
    "Open a bound pack, or read all decoded members as pill views. Unseal compressed packs first."
  ],
  [
    "Pack",
    "pack.push(pill) / pack.push(bytes)",
    "Append in place; keep the packing options."
  ],
  [
    "Pack",
    "pack.pop()",
    "Remove and return the last pill; an empty pack returns no pill."
  ],
  [
    "Pack",
    "pack.get(ordinal) / pack.offset(byteOffset)",
    "Read without removing. Ordinals start at zero; offsets are exact member-relative byte positions."
  ],
  [
    "Pack",
    "pack.seal(compression)",
    "Return a sealed wrapper. The original pack and its views remain valid."
  ],
  [
    "Sealed Pack",
    "cpack.unseal()",
    "Restore a mutable pack and its representation."
  ],
  [
    "Pack",
    "pack.unpack()",
    "Return logical pill views, including inherited common fields."
  ],
  [
    "Pill",
    "pill.field(name) / pill.field(position)",
    "Read encoded field bytes, or an explicit absent/null state."
  ],
  [
    "Pill",
    "pill.json()",
    "Render JSON text only when requested."
  ]
];

export const libraryNotes = [
  "Raw bytes default to .rx / PIL: doc.write(definitionBytes). JavaScript JSON text implies JSON; a browser File uses its .rx, .pil, .json, or .cbor extension. Override ambiguous input with doc.write(bytes, \"cbor\"). Go WriteFile and Rust write_file infer the same extensions; Rust uses write_as for an explicit format.",
  "Pill Packer treats application headers as opaque bytes. Seeds resolves Trie-Trunk time bases, branch rows, and state-trie fields directly over borrowed member spans. Generic member addressing and compression remain in Pill Packer.",
  "Rust and Go use idiomatic capitalization and explicit variants where overloading is unavailable. Rust uses Fill::Encoded, Fill::Fields, and Fill::Empty; raw-byte appends use push_bytes. Go Fill accepts encoded bytes or ordered field buffers.",
  "fill(bytes) binds one already-encoded PIL record without copying its host buffer. Validation still runs; JavaScript uploads bytes into reusable WASM scratch memory. Field reads borrow encoded bytes rather than constructing event objects. Keep borrowed buffers immutable while views use them.",
  "Empty field bytes, absent, and null are distinct. JavaScript uses absent and nullValue, Rust uses FieldBytes, and Go supports explicit codec.Value states. Unknown states are rejected. Application ID helpers belong in the Seed layer.",
  "push and pop mutate the pack, retain record references, and defer serialization until bytes or seal is requested. Fixed width, directory width, named headers, and common-factoring options survive these mutations and the in-memory seal/unseal round trip. Reopening serialized bytes recovers the encoded representation; a factoring policy that produced no common fields cannot be inferred from the wire.",
  "Gzip compression produces another buffer; seal does not overwrite the original pack. onlyIfSmaller explicitly permits an uncompressed result. Rust unseal consumes the wrapper and can move its uncompressed buffer. Release JavaScript prescriptions with dispose() and Go prescriptions with Close() after their views are finished; Rust uses ownership.",
  "Prepare a prescription once for repeated work. doc.read is a convenience that prepares it again. Bulk fixed-width and indexed constructors avoid creating a pill for every append. For direct application views, JavaScript provides view(ordinal, sink), popView(sink), and ordinalAtOffset(offset). For repeated reads, Go provides GetInto, OffsetInto, and FieldWith; Rust provides fill_with and field_with with reusable scratch storage. Dynamic common-field discovery can still require whole-pack checks."
];

export const librarySpecification = `### 12. Prescription Library API

These examples accept PIL Definition bytes (.rx) and an encoded PIL record. JavaScript doc initializes and reuses the packaged WASM runtime automatically. Rust requires the api and gzip features; Go links the Rust native library.

| Owner | Operation | Meaning |
| --- | --- | --- |
${prescriptionOperations.map(row=>`| ${row.join(' | ')} |`).join('\n')}

${libraryNotes.join('\n\n')}

#### Bulk Addressing and Headers

JavaScript accepts rx.pack(headers, recordBytes, recordWidth) and rx.pack(headers, addressBytes, offsets, recordBytes). Rust uses pack_fixed / pack_indexed; Go uses PackFixed / PackIndexed. Directory widths are 2, 3, or 4 bytes. Supplied offsets are little-endian, begin at zero, and describe every record; mutation regenerates the directory. Ordinals and offsets refer to the current pack. Keep published packs immutable while application indexes refer to them.

encodeHeaders (Rust encode_headers; Go EncodeHeaders) encodes a PIL map of text names to byte values. The library generates structural fields. The 64-byte PAC prefix contains 32 bytes of framing and a 32-byte SHA-256 Definition identity, not a payload checksum. CRC32C and gzip integrity checks do not replace that binding.

${libraryExamples.map(([id,label,code])=>`#### ${label}\n\n\`\`\`${id}\n${code}\n\`\`\``).join('\n\n')}
`;
export function pilPage(arrow){return `
<section class="wrap pil-hero"><p class="project-line">PIL / Positional Interchange Layout</p><h1>Positional Interchange Layout</h1><p class="intro">Typed binary records. Shared Definitions. Compact packs.<br>Independent of the datastore that carries them.</p><div class="actions"><a class="button" href="/pill-packer.txt">Read the Specification ${arrow}</a><a class="text-link" href="https://github.com/AuthenticityLabs/pill-packer">Pill Packer on GitHub ${arrow}</a></div></section>
<section class="wrap layout-section" id="layout"><div class="section-heading"><h2>Record Layout</h2><p>Widths follow the Definition<br>and the fields present in each record.</p></div>
<div class="pil-lane-labels" aria-hidden="true"><span>Field</span><span>Fixed</span><span>Variable</span></div>
<div class="byte-lane">${[['Field','States','2 bits per field'],['Field','Lengths','Optional integer widths'],['Fixed','Values','Scalars and dictionary IDs'],['Variable','Offsets','One per variable value'],['Variable','Values','Text, bytes, objects, arrays']].map(([group,name,detail],i)=>`<div role="group" aria-label="${group} ${name}"><small>0${i+1}</small><strong><span class="pil-lane-mobile-group">${group}</span>${name}</strong><span>${detail}</span></div>`).join('')}</div>

<h3>One Record, 12 Bytes</h3><pre class="pil-record"><code>${escape(JSON.stringify(layoutRecord))}</code></pre><p class="pil-copy">This Definition uses fixed integer widths, so the integer-width mask is omitted. All byte positions below start at zero.</p>
<div class="pil-byte-example" aria-label="Twelve-byte PIL record with byte positions">${[['0','AB','Field States','10 · 10 · 10 · 11'],['1–2','2A 00','id','42 · U16'],['3–6','DC 05 00 00','count','1500 · U32'],['7','01','state','Dictionary entry 1'],['8–9','02 00','Offset','2 bytes into variable data'],['10–11','4F 4B','note','OK · UTF-8']].map(([pos,hex,label,detail])=>`<div><span class="pil-position">Byte ${pos}</span><code>${hex}</code><strong>${label}</strong><span>${detail}</span></div>`).join('')}</div>
<div class="pil-explain-grid"><div><h3>Read the Mask</h3><p><code>AB</code> is binary <code>10 10 10 11</code>. Read from the high bits: <code>id</code>, <code>count</code>, and <code>state</code> use fixed-width values; <code>note</code> uses an offset. Four fields fit in one mask byte.</p></div><div><h3>Read the Values</h3><p><code>2A 00</code> is 42 and <code>DC 05 00 00</code> is 1500 in little-endian order. The dictionary turns <code>01</code> into <code>authorized</code>. No copy of that string is stored in the record.</p></div><div><h3>Find the Variable Data</h3><p>The variable section starts at byte 10. Its first value starts at relative offset 0; the end offset is 2. Bytes 10–11 are <code>4F 4B</code>, or <code>OK</code>. Each later value starts at the previous value’s end.</p></div></div>
${table(['State Bits','Meaning','Value Storage'],[['<code>00</code>','Absent','No value bytes'],['<code>01</code>','Null','No value bytes'],['<code>10</code>','Fixed-Width Value','Value or dictionary ID in the fixed section'],['<code>11</code>','Offset','End offset plus data in the variable section']])}
<p class="pil-copy">Absent and null are distinct. Neither reserves a value slot. The state mask uses <code>ceil(field count / 4)</code> bytes; unused low bits are zero. There is no standalone record-length prefix: masks, widths, and the final end offset determine its boundary.</p>

<h3>How Types Occupy the Layout</h3>
${table(['Representation','Section','Encoding'],[['Integers, floats, UUIDs, timestamps','Fixed','Declared width; eligible integers may use a separate width mask'],['Dictionary-backed strings','Fixed or Variable','Dictionary ID, or literal UTF-8 bytes'],['Text and bytes','Variable','Contiguous data addressed by cumulative end offsets'],['Fixed-capacity text','Fixed','Explicit byte length + UTF-8 content + zero padding to capacity'],['Arrays','Variable','Count followed by elements; variable elements have end offsets'],['Nested records','Variable','Schema-bound child PIL frames; the schema supplies child layouts']])}
<details class="pil-detail" id="cddl"><summary>Schema Mapping</summary><p>Named records, optional fields, homogeneous arrays, choices, literals, bounded integers, and supported size constraints compile into positional layouts. Records can preserve additional fields with <code>* tstr =&gt; any</code>. The compiler rejects unsupported CDDL constructs, including recursive rules, arbitrary dynamic maps, generics, group rules, and tuple arrays.</p><p>CDDL constraints describe valid values. Storage annotations select representations such as fixed-capacity text without changing those constraints. Open <code>OBJECT</code> values use the built-in PIL value layout. The specification describes the exact mapping and encoding rules.</p></details></section>
<section class="wrap seed-section" id="packs"><h2>PAC Layout</h2><p class="seed-lead">A Packed Array Container binds ordered records to one exact Definition.</p><div class="pil-pack-flow"><div>Fixed Header <span>64 bytes · Lengths, flags, count, Definition digest</span></div><div>Header Data <span>Extensions, layout controls, named headers, common frame, optional directory</span></div><div>PIL Members <span>Record 0 → Record 1 → Record 2 → …</span></div></div>
${table(['Byte Position','Bytes','Header Value'],[['0–3','4','Decoded container length'],['4–7','4','Flags'],['8–15','8','Total decoded PAC length'],['16–23','8','Header length / first member position'],['24–31','8','Member count'],['32–63','32','SHA-256 digest of exact Definition bytes']])}
<div class="pil-explain-grid"><div><h3>Shared Values</h3><p>A common PIL frame holds values shared by every member at any field position. Those positions are Absent in members. Reading a logical record combines its member values with the common frame.</p></div><div><h3>Member Addressing</h3><p>Sequential packs scan self-delimiting frames. An optional directory provides 2-, 3-, or 4-byte member-relative offsets. A declared fixed record width instead permits direct ordinal addressing after every member’s width is verified.</p></div><div><h3>Compression</h3><p>The entire decoded PAC can be gzip-compressed. A 16-byte wrapper carries stored length, codec, decoded length, and member count. Expand the PAC before addressing its members.</p></div></div><p class="pil-copy">The 64-byte prefix is 32 bytes of framing plus 32 bytes identifying the exact Definition. SHA-256 binds the layout; it is not a payload checksum. CRC32C and gzip integrity checks detect damaged bytes and do not replace that binding. Full reads validate structure and values; trusted reads still check bounds and framing.</p></section>
<section class="wrap seed-section pil-libraries" id="libraries"><h2>Pill Packer Libraries</h2><p class="seed-lead">Shared binary records across Rust, JavaScript/WASM, and Go.</p><p class="pil-copy">The inputs are an <code>.rx</code> Definition (<code>cddl.rx</code>), an encoded record (<code>pill_bytes</code>), and encoded named headers (<code>header_bytes</code>). Prepare <code>rx</code> once, fill a <code>pill</code>, then <code>pack</code> and seal it into <code>cpack</code>.</p>
<div class="pil-library-code" data-pil-tabs>
${libraryExamples.map(([id,label,code],i)=>`<div id="library-${id}" role="tabpanel" aria-labelledby="library-tab-${id}" tabindex="0"${i?' hidden':''}><pre><code id="library-code-${id}">${highlight(code)}</code></pre></div>`).join('')}
<div class="pil-code-footer"><div class="pil-language-tabs" role="tablist" aria-label="Library Language">${libraryExamples.map(([id,label],i)=>`<button type="button" role="tab" id="library-tab-${id}" aria-controls="library-${id}" aria-selected="${i===0}" tabindex="${i===0?0:-1}">${label}</button>`).join('')}</div>${copyButton('library-code-javascript','Copy JavaScript / WASM Example')}</div></div>
<p class="pil-copy">JavaScript loads and reuses the packaged WASM runtime automatically. Rust requires the <code>api</code> and <code>gzip</code> features. Go links the Rust native library.</p><a class="text-link" href="https://github.com/AuthenticityLabs/pill-packer#build--test">Library Setup &amp; Build Instructions ${arrow}</a>
<h3>Prescriptions, Pills, and Packs</h3>
${table(['Owner','Operation','Result'],prescriptionOperations.map(([owner,operation,result])=>[owner,`<code>${escape(operation)}</code>`,result]))}
<details class="pil-detail"><summary>Ownership and Performance</summary>${libraryNotes.map(note=>`<p>${escape(note)}</p>`).join('')}</details>
<details class="pil-detail"><summary>Bulk Addressing and Named Headers</summary><p>Pass fixed-width records with <code>rx.pack(headers, recordBytes, recordWidth)</code>, or supply a directory with <code>rx.pack(headers, addressBytes, offsets, recordBytes)</code>. Rust uses <code>pack_fixed</code> / <code>pack_indexed</code>; Go uses <code>PackFixed</code> / <code>PackIndexed</code>.</p><p>Directory widths are 2, 3, or 4 bytes. Supplied offsets are little-endian, start at zero, and describe every record; mutation regenerates the directory. Keep published packs immutable while application indexes refer to them.</p><p><code>encodeHeaders</code> (Rust <code>encode_headers</code>; Go <code>EncodeHeaders</code>) encodes a PIL map of text names to byte values. The library supplies count, lengths, flags, and Definition identity.</p></details></section>
<section class="wrap seed-section" id="definition"><h2>Definitions</h2><p class="seed-lead">CDDL describes the data. The compiled PIL Definition binds field positions, types, dictionaries, and validation rules. JSON provides an editable view of that Definition.</p>
<div class="pil-notebook" data-pil-tabs data-pil-deep-links>
<div class="pil-definition-tabs" role="tablist" aria-label="Definitions" aria-orientation="vertical">${[['cli','CLI'],['editor','Editor'],['schema','Schema'],['agents','Agents']].map(([id,label],i)=>`<button type="button" role="tab" id="definition-tab-${id}" aria-controls="definition-${id}" aria-selected="${i===0}" tabindex="${i===0?0:-1}">${label}</button>`).join('')}</div>
<div class="pil-notebook-pages">
<div id="definition-cli" role="tabpanel" aria-labelledby="definition-tab-cli" tabindex="0">
<h3 id="reader">Read, Write, &amp; Fill Prescriptions with RX</h3><p class="seed-lead">Read records as JSONL, write Definitions from CDDL, and fill records through the codec library.</p>
<div class="rx-install"><h3>Install RX &amp; DOC</h3><p class="pil-copy">macOS · Linux · Windows via WSL. Downloads the source and compiles RX and DOC for your machine. Installs Rust if needed; requires your OS compiler tools.</p><div class="query-example"><div class="example-title"><span>Terminal</span><button class="code-copy" type="button" data-copy-code="rx-install-command" aria-label="Copy Install Command"><svg class="copy-icon" viewBox="0 0 24 24" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg><svg class="copied-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12 4 4L19 6"/></svg><span data-copy-feedback role="status" aria-live="polite">Copy</span></button></div><pre><code id="rx-install-command">curl -fsSL https://trietrunk.com/install-rx.sh | sh</code></pre></div><p class="pil-copy">Installs into <code>~/.cargo/bin</code> by default. <a href="/install-rx.sh">View the Installer</a></p></div>
<div class="query-example"><div class="example-title">Write a Prescription, Then Inspect It</div><pre><code>rx write purchase.cddl purchase.rx --root purchase
rx read purchase.rx --pretty</code></pre></div><p class="pil-copy">Start with the <a href="/purchase.cddl">example CDDL source</a>. The first command compiles it into <code>purchase.rx</code>; the second renders that binary Definition as readable JSON. <code>.rx</code> is the compiled schema used by the codec.</p>
<details class="pil-detail"><summary>Try the Worked Example</summary><p>These sample files belong to the 12-byte record shown above. Download the JSON schema and the data you want to read into one folder. Compile your own prescription, then inspect the records:</p><div class="actions"><a class="text-link" href="/pil-example.json" download>Example Schema (JSON) ${arrow}</a><a class="text-link" href="/pil-example.pil" download>Example Record (PIL) ${arrow}</a><a class="text-link" href="/pil-example.pac" download>Example Pack (PAC) ${arrow}</a><a class="text-link" href="/pil-example.compressed.pac" download>Example Compressed Pack ${arrow}</a></div><pre><code>rx write pil-example.json pil-example.rx
rx read pil-example.rx pil-example.pil
rx read pil-example.rx pil-example.pac
rx read pil-example.rx pil-example.compressed.pac --pretty</code></pre></details>

<p class="pil-copy">Omit the data path to inspect a Definition. <code>--pretty</code> indents JSON output. Record filling uses the prescription’s <code>rx.fill</code> API; the CLI provides <code>read</code> and <code>write</code>.</p><a class="text-link" href="/pill-packer.txt">Complete Specification & Commands ${arrow}</a>
</div>
<div id="definition-editor" role="tabpanel" aria-labelledby="definition-tab-editor" tabindex="0" hidden>
<h3 id="doc-editor">Shape a Definition with DOC Editor</h3><p class="pil-copy">Choose a type, then its range or size. Review optional fields, nulls, and dictionaries in the terminal editor, or edit CDDL directly. JSON samples suggest a starting point; you decide the constraints. Every saved Definition is <code>.rx</code>.</p>
${codeBox("doc-inputs","Open an Input","doc\ndoc --json samples.json\ndoc --cddl purchase.cddl\ndoc --rx purchase.rx")}<p class="pil-copy">Run <code>doc</code> to start from scratch. Use the field controls to choose types and sizes, or edit CDDL directly. Review sample-derived choices before saving.</p>
${table(["Key","Action"],[["F2","Switch Fields, CDDL, Samples, and Layout"],["Enter","Edit the selected field"],["F5","Apply a dialog or validate samples"],["Ctrl+S","Save the Definition as RX"]])}<a class="text-link" href="https://github.com/AuthenticityLabs/pill-packer#doc-definition-editor">DOC Editor Reference ${arrow}</a>

</div>
<div id="definition-schema" role="tabpanel" aria-labelledby="definition-tab-schema" tabindex="0" hidden>
<div class="pil-binding"><div><strong>Definition</strong><span>Identity · Version · Ordered Fields · Schema</span></div><span aria-hidden="true">→</span><div><strong>PIL Records / PAC</strong><span>Field states and values under that exact Definition</span></div></div>
<p class="pil-copy">Records do not repeat field names or their Definition. A PAC identifies its Definition with a SHA-256 digest; a standalone stream receives its binding from the application.</p>
<h3>Four Fields, Four Positions</h3>
${table(['Position','Field','Type','Dictionary'],[['0','<code>id</code>','<code>U16</code>','—'],['1','<code>count</code>','<code>U32</code>','—'],['2','<code>state</code>','<code>STRING</code>','<code>0 → pending<br>1 → authorized<br>2 → canceled</code>'],['3','<code>note</code>','<code>STRING</code>','—']])}
<p class="pil-copy">Field position identifies the field. Dictionary position identifies its string value. Here, <code>01</code> represents <code>authorized</code> in one byte. Dictionary IDs use 1, 2, 4, or 8 bytes according to dictionary size; a string can also be supplied literally in the variable section.</p>
<details class="pil-detail"><summary>JSON View of This Definition</summary><pre><code>${escape(JSON.stringify(layoutDefinition,null,2))}</code></pre><a class="text-link" href="/pil-example.json" download>Download Definition JSON ${arrow}</a></details>
<p class="pil-copy">Definitions are immutable. Changing field order, types, or dictionary entries requires a different exact binding. The <code>version</code> identifies the Definition revision; <code>pil</code> selects its integer layout.</p>
<a class="text-link" href="/definition.cddl">Read the Definition Schema ${arrow}</a>
</div>
<div id="definition-agents" role="tabpanel" aria-labelledby="definition-tab-agents" tabindex="0" hidden>
<h3>Teach Coding Agent Definition Creation</h3><p class="pil-copy">Replace the placeholders, then copy the instructions to your coding agent.</p>
<div class="pil-agent-card">${copyButton('definition-agent-prompt','Copy Agent Instructions')}<details class="pil-agent-details"><summary>View Agent Instructions</summary><pre><code id="definition-agent-prompt">${escape(definitionAgentPrompt)}</code></pre></details></div>
</div></div></div></section>
`;}

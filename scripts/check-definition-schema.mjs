// Compile the portable schema and Trie-Trunk profile into one CDDL entry point.
import {mkdtempSync, readFileSync, writeFileSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {execFileSync} from 'node:child_process';
const root = fileURLToPath(new URL('../', import.meta.url));
const library = new URL('../../pill-packer/', import.meta.url);
const temp = mkdtempSync(join(tmpdir(), 'trunk-cddl-'));
try {
  const schema = join(temp, 'definition.cddl');
  writeFileSync(schema, 'entry = trunk-definition\n' + readFileSync(new URL('schemas/definition.cddl', library), 'utf8') + readFileSync(new URL('../schemas/trunk-definition.cddl', import.meta.url), 'utf8'));
  execFileSync('cargo', ['run', '--locked', '--manifest-path', fileURLToPath(new URL('rust/Cargo.toml', library)), '--example', 'check_definition', '--', schema, ...['native-pil','native-ranges','pac-view'].map(n=>`client/services/fixtures/${n}/definition.rx`)], {cwd:root, stdio:'inherit'});
} finally { rmSync(temp, {recursive:true, force:true}); }

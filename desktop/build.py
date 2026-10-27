#!/usr/bin/env python3
"""Build the native macOS app; Node is used only by the existing frontend build."""
import argparse, hashlib, json, os, pathlib, plistlib, shutil, subprocess, tempfile, urllib.request, zipfile
ROOT = pathlib.Path(__file__).resolve().parents[1]
VERSION = '6.10.0'
SHA256 = 'e633f480737f229f582d8ec6165a52d789340950aef062560702122389f08329'
def run(*args, **kwargs):
    subprocess.run([str(a) for a in args], check=True, **kwargs)
def main():
    ap=argparse.ArgumentParser();ap.add_argument('--arch',choices=['arm64','x64','universal'],default='universal');ap.add_argument('--skip-web',action='store_true');args=ap.parse_args()
    cache=ROOT/'desktop/cache';cache.mkdir(parents=True,exist_ok=True)
    archive=cache/f'neutralinojs-v{VERSION}.zip'
    if not archive.exists():
        urllib.request.urlretrieve(f'https://github.com/neutralinojs/neutralinojs/releases/download/v{VERSION}/{archive.name}',archive)
    if hashlib.sha256(archive.read_bytes()).hexdigest()!=SHA256:raise SystemExit('Neutralino archive checksum mismatch')
    if not args.skip_web:run('npm','--prefix',ROOT/'client','run','build',cwd=ROOT)
    if not (ROOT/'web/index.html').exists():raise SystemExit('Build the Snoot frontend first')
    app=ROOT/'desktop/dist/Snoot.app';contents=app/'Contents';mac=contents/'MacOS';resources=contents/'Resources'
    if app.exists():shutil.rmtree(app)
    mac.mkdir(parents=True);resources.mkdir(parents=True)
    with zipfile.ZipFile(archive) as z:(mac/'neutralino').write_bytes(z.read(f'neutralino-mac_{args.arch}'))
    (mac/'neutralino').chmod(0o755)
    env=dict(os.environ,CGO_ENABLED='0',GOOS='darwin',GOCACHE=os.environ.get('GOCACHE','/tmp/trunk-go-build-cache'))
    arches=['arm64','amd64'] if args.arch=='universal' else ['amd64' if args.arch=='x64' else 'arm64']
    binaries=[]
    for arch in arches:
        binary=cache/f'snoot-{arch}';run('go','build','-trimpath','-ldflags=-s -w','-o',binary,'./cmd/snoot-desktop',cwd=ROOT/'go',env=dict(env,GOARCH=arch));binaries.append(binary)
    if len(binaries)==2:run('lipo','-create',*binaries,'-output',mac/'snoot')
    else:shutil.copy2(binaries[0],mac/'snoot')
    (mac/'snoot').chmod(0o755)
    shutil.copytree(ROOT/'web',resources/'web',ignore=shutil.ignore_patterns('bench-native'))
    shutil.copy2(ROOT/'desktop/neutralino.config.json',resources/'neutralino.config.json')
    shutil.copy2(ROOT/'LICENSE',resources/'LICENSE')
    shutil.copy2(ROOT/'desktop/LICENSE.neutralino',resources/'LICENSE.neutralino')
    with tempfile.TemporaryDirectory(prefix='snoot-icon-') as temporary:
        iconset=pathlib.Path(temporary)/'Snoot.iconset';iconset.mkdir()
        for size in [16,32,128,256,512]:
            for scale in [1,2]:
                name=f'icon_{size}x{size}'+('@2x' if scale==2 else '')+'.png'
                run('sips','-z',size*scale,size*scale,ROOT/'client/assets/snoot-forward.png','--out',iconset/name,stdout=subprocess.DEVNULL)
        run('iconutil','-c','icns',iconset,'-o',resources/'Snoot.icns')
    info={'CFBundleIdentifier':'com.authenticitylabs.snoot','CFBundleName':'Snoot','CFBundleDisplayName':'Snoot','CFBundleExecutable':'snoot','CFBundleIconFile':'Snoot.icns','CFBundlePackageType':'APPL','CFBundleShortVersionString':'0.1.0','CFBundleVersion':'1','LSMinimumSystemVersion':'14.0','NSHighResolutionCapable':True,'NSAppTransportSecurity':{'NSAllowsLocalNetworking':True},'NSPrincipalClass':'NSApplication'}
    (contents/'Info.plist').write_bytes(plistlib.dumps(info))
    for binary in [mac/'neutralino',mac/'snoot']:run('codesign','--force','--sign','-',binary)
    run('codesign','--force','--sign','-',app)
    run('codesign','--verify','--deep','--strict',app)
    output=app.parent/f'Snoot-macOS-{args.arch}.zip'
    if output.exists():output.unlink()
    run('ditto','-c','-k','--sequesterRsrc','--keepParent',app,output)
    print(app);print(output)
if __name__=='__main__':main()

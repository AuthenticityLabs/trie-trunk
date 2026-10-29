#!/usr/bin/env python3
"""Build the native macOS app; Node is used only by the existing frontend build."""
import argparse, os, pathlib, plistlib, shutil, subprocess, tempfile
from runtime import build_runtime
ROOT = pathlib.Path(__file__).resolve().parents[1]
def run(*args, **kwargs):
    subprocess.run([str(a) for a in args], check=True, **kwargs)
def main():
    ap=argparse.ArgumentParser();ap.add_argument('--arch',choices=['arm64','x64','universal'],default='universal');ap.add_argument('--min-macos',choices=['13.7','14.0'],default='14.0');ap.add_argument('--skip-web',action='store_true');ap.add_argument('--output-dir',type=pathlib.Path,default=ROOT/'desktop/dist');args=ap.parse_args()
    cache=ROOT/'desktop/cache';cache.mkdir(parents=True,exist_ok=True)
    runtime=build_runtime(ROOT,args.arch,args.min_macos)
    if not args.skip_web:run('npm','--prefix',ROOT/'client','run','build',cwd=ROOT)
    if not (ROOT/'web/index.html').exists():raise SystemExit('Build the Snoot frontend first')
    app=args.output_dir.resolve()/'Snoot.app';contents=app/'Contents';mac=contents/'MacOS';resources=contents/'Resources'
    if app.exists():shutil.rmtree(app)
    mac.mkdir(parents=True);resources.mkdir(parents=True)
    shutil.copy2(runtime,mac/'neutralino')
    (mac/'neutralino').chmod(0o755)
    env=dict(os.environ,CGO_ENABLED='0',GOOS='darwin',GOAMD64='v1',GOCACHE=os.environ.get('GOCACHE','/tmp/trunk-go-build-cache'))
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
    info={'CFBundleIdentifier':'com.authenticitylabs.snoot','CFBundleName':'Snoot','CFBundleDisplayName':'Snoot','CFBundleExecutable':'snoot','CFBundleIconFile':'Snoot.icns','CFBundlePackageType':'APPL','CFBundleShortVersionString':'0.1.0','CFBundleVersion':'1','LSMinimumSystemVersion':args.min_macos,'NSHighResolutionCapable':True,'NSAppTransportSecurity':{'NSAllowsLocalNetworking':True},'NSPrincipalClass':'NSApplication'}
    (contents/'Info.plist').write_bytes(plistlib.dumps(info))
    for binary in [mac/'neutralino',mac/'snoot']:run('codesign','--force','--sign','-',binary)
    run('codesign','--force','--sign','-',app)
    run('codesign','--verify','--deep','--strict',app)
    output=app.parent/f'Snoot-macOS-{args.arch}.zip'
    if output.exists():output.unlink()
    run('ditto','-c','-k','--sequesterRsrc','--keepParent',app,output)
    print(app);print(output)
if __name__=='__main__':main()

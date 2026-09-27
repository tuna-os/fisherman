#!/usr/bin/env python3
"""Real small local OCI/Podman/Skopeo ID transport; no registry image or OS boot."""
import hashlib,io,json,os,subprocess,tarfile,tempfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
def run(argv,**options):
    result=subprocess.run(argv,capture_output=True,text=True,timeout=60,**options)
    if result.returncode:raise RuntimeError(str(argv[:2])+': exit '+str(result.returncode)+': '+result.stderr[-4096:])
    return result

def main():
    with tempfile.TemporaryDirectory(prefix='.native-local-',dir=ROOT) as temporary,tempfile.TemporaryDirectory(prefix='fish-run-') as runtime:
        folder=Path(temporary);layout=folder/'oci';blobs=layout/'blobs/sha256';blobs.mkdir(parents=True)
        def blob(raw,kind):
            digest=hashlib.sha256(raw).hexdigest();(blobs/digest).write_bytes(raw)
            return {'digest':'sha256:'+digest,'size':len(raw),'mediaType':kind}
        buffer=io.BytesIO()
        with tarfile.open(fileobj=buffer,mode='w') as archive:
            item=tarfile.TarInfo('proof');item.size=5;archive.addfile(item,io.BytesIO(b'proof'))
        layer=blob(buffer.getvalue(),'application/vnd.oci.image.layer.v1.tar')
        config=blob(json.dumps({'architecture':'amd64','os':'linux','config':{},'rootfs':{'type':'layers','diff_ids':[layer['digest']]}}).encode(),'application/vnd.oci.image.config.v1+json')
        manifest=blob(json.dumps({'schemaVersion':2,'mediaType':'application/vnd.oci.image.manifest.v1+json','config':config,'layers':[layer]}).encode(),'application/vnd.oci.image.manifest.v1+json')
        (layout/'index.json').write_text(json.dumps({'schemaVersion':2,'manifests':[manifest]}));(layout/'oci-layout').write_text('{"imageLayoutVersion":"1.0.0"}')
        store=folder/'store';podman=['/usr/bin/podman','--root',str(store),'--runroot',runtime,'--storage-driver','vfs']
        run(podman+['pull','-q','oci:'+str(layout)])
        identity=config['digest'][7:];run(podman+['image','exists',identity])
        observed=json.loads(run(podman+['image','inspect',identity]).stdout)
        if len(observed)!=1 or observed[0]['Id']!=identity or observed[0]['Digest']!=manifest['digest']:raise ValueError('actual imported identity differs: '+str([(v.get('Id'),v.get('Digest')) for v in observed])[:512])
        storage=folder/'storage.conf';storage.write_text('[storage]\ndriver="vfs"\ngraphroot='+json.dumps(str(store))+'\nrunroot='+json.dumps(runtime)+'\n')
        tools=folder/'tools';tools.mkdir();marker=folder/'forbidden';calls=folder/'queries'
        guard=tools/'skopeo';guard.write_text('#!/bin/bash\nprintf "%s\\n" "$*" >> "$QUERIES"\nif [[ $# != 2 || $1 != inspect || $2 != "$EXPECTED" ]]; then touch "$FORBIDDEN";exit 97;fi\nexec /usr/bin/skopeo "$@"\n');guard.chmod(0o700)
        module=ROOT/'fisherman';source_ref='containers-storage:'+identity
        env=dict(os.environ,CONTAINERS_STORAGE_CONF=str(storage),PATH=str(tools)+':/usr/bin:/bin',EXPECTED=source_ref,FORBIDDEN=str(marker),QUERIES=str(calls))
        direct=json.loads(run(['/usr/bin/skopeo','inspect',source_ref],env=env).stdout)
        if direct['Digest']!=manifest['digest']:raise ValueError('actual default storage ID digest differs: expected '+manifest['digest']+' observed '+str(direct.get('Digest'))[:128])
        with tempfile.TemporaryDirectory(prefix='.local-check-',dir=module) as source_dir:
            source=Path(source_dir)/'main.go';source.write_text('package main\nimport("encoding/json";"os";"github.com/tuna-os/fisherman/internal/install")\nfunc main(){json.NewEncoder(os.Stdout).Encode(install.CheckImage(os.Args[1]))}\n')
            executable=folder/'check';run(['go','build','-o',str(executable),str(source)],cwd=module)
            result=run([str(executable),source_ref],env=env);check=json.loads(result.stdout.splitlines()[-1])
        if marker.exists() or check['NeedsPull'] or not check['Offline'] or check['LayerCount']!=1:raise ValueError('actual local-ID check queried registry or failed')
        if calls.read_text()!='inspect '+source_ref+'\n':raise ValueError('unexpected actual lookup count/source')
        print(json.dumps({'schemaVersion':1,'complete':True,'scope':'actual tiny owned-store image ID import/transport and compiled cache check','sourceCommit':run(['git','rev-parse','HEAD'],cwd=ROOT).stdout.strip(),'bootcSourceSha256':hashlib.sha256((module/'internal/install/bootc.go').read_bytes()).hexdigest(),'configId':identity,'manifestDigest':manifest['digest'],'observedPodmanDigest':observed[0]['Digest'],'observedSkopeoDigest':direct['Digest'],'actualCheckImage':check,'registryLookupAttempted':False,'selectedOsAcquired':False,'selectedOsBootAcceptance':False,'vmExecuted':False},sort_keys=True))
if __name__=='__main__':
    try:main()
    except Exception as error:
        print(json.dumps({'schemaVersion':1,'complete':False,'failure':type(error).__name__,'detail':str(error)[:4096],'selectedOsAcquired':False,'selectedOsBootAcceptance':False,'vmExecuted':False},sort_keys=True))
        raise SystemExit(1)

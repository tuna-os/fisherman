#!/usr/bin/env python3
"""Real small local OCI/Podman/Skopeo ID transport; no registry image or OS boot."""
import hashlib,io,json,os,subprocess,tarfile,tempfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
import importlib.util,contextlib,shutil
spec=importlib.util.spec_from_file_location('owned',ROOT/'tests/native-process.py')
OWNER=importlib.util.module_from_spec(spec);spec.loader.exec_module(OWNER)
RECEIPT=OWNER.RECEIPT
@contextlib.contextmanager
def scratch(prefix,dir=None):
    path=tempfile.mkdtemp(prefix=prefix,dir=dir)
    try:yield path
    finally:
        if any(c.get('processStarted') and not c.get('reap',{}).get('groupEmptyObserved') for c in RECEIPT.get('commands',[])):
            RECEIPT.setdefault('preservedScratch',[]).append(path)
        else:
            try:shutil.rmtree(path)
            except OSError as error:
                RECEIPT.setdefault('cleanupFailures',[]).append({'path':path,'failureClass':type(error).__name__})
                RECEIPT.setdefault('preservedScratch',[]).append(path)
def run(argv,stage,**options):
    return OWNER.run(argv,stage,**options)
def bind():
    RECEIPT.update(schemaVersion=1,complete=False,workflowSource=os.environ.get('GITHUB_SHA'),
        runId=os.environ.get('GITHUB_RUN_ID'),runAttempt=os.environ.get('GITHUB_RUN_ATTEMPT'),
        hostBootId=Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
        uid=os.getuid(),selectedOsAcquired=False,selectedOsBootAcceptance=False,vmExecuted=False,
        sourceHashes={},tools={})
    for name in ('tests/native-local-id.py','tests/native-process.py','fisherman/internal/install/bootc.go','fisherman/cmd/fisherman/main.go'):
        RECEIPT['sourceHashes'][name]=hashlib.sha256((ROOT/name).read_bytes()).hexdigest()
    for name in ('podman','skopeo','go','git'):
        path=shutil.which(name)
        RECEIPT['tools'][name]={'resolvedPath':path,'sha256':None}
        if path:
            target=Path(path).resolve();before=target.stat()
            if not target.is_file() or before.st_size>268435456:raise ValueError('tool file exceeds bound')
            with target.open('rb') as stream:
                digest=hashlib.file_digest(stream,'sha256').hexdigest()
            after=target.stat()
            if (before.st_dev,before.st_ino,before.st_size,before.st_mtime_ns)!=(after.st_dev,after.st_ino,after.st_size,after.st_mtime_ns):raise ValueError('tool changed during binding')
            RECEIPT['tools'][name].update(resolvedPath=str(target),sha256=digest)
    RECEIPT['sourceCommit']=run(['git','rev-parse','HEAD'],'source-observation',cwd=ROOT).stdout.strip()
    missing=[name for name,value in RECEIPT['tools'].items() if not value['resolvedPath']]
    if missing:raise FileNotFoundError('required native tools absent: '+','.join(missing))

def main():
    with scratch(prefix='.native-local-',dir=ROOT) as temporary,scratch(prefix='fish-run-') as runtime:
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
        run(podman+['pull','-q','oci:'+str(layout)],'import-tiny-oci')
        identity=config['digest'][7:];run(podman+['image','exists',identity],'local-id-exists')
        observed=json.loads(run(podman+['image','inspect',identity],'imported-identity-readback').stdout)
        if len(observed)!=1 or observed[0]['Id']!=identity or observed[0]['Digest']!=manifest['digest']:raise ValueError('actual imported identity differs: '+str([(v.get('Id'),v.get('Digest')) for v in observed])[:512])
        storage=folder/'storage.conf';storage.write_text('[storage]\ndriver="vfs"\ngraphroot='+json.dumps(str(store))+'\nrunroot='+json.dumps(runtime)+'\n')
        tools=folder/'tools';tools.mkdir();marker=folder/'forbidden';calls=folder/'queries'
        guard=tools/'skopeo';guard.write_text('#!/bin/bash\nprintf "%s\\n" "$*" >> "$QUERIES"\nif [[ $# != 2 || $1 != inspect || $2 != "$EXPECTED" ]]; then touch "$FORBIDDEN";exit 97;fi\nexec /usr/bin/skopeo "$@"\n');guard.chmod(0o700)
        module=ROOT/'fisherman';source_ref='containers-storage:'+identity
        env=dict(os.environ,CONTAINERS_STORAGE_CONF=str(storage),PATH=str(tools)+':/usr/bin:/bin',EXPECTED=source_ref,FORBIDDEN=str(marker),QUERIES=str(calls))
        direct=json.loads(run(['/usr/bin/skopeo','inspect',source_ref],'skopeo-local-id-readback',env=env).stdout)
        if direct['Digest']!=manifest['digest']:raise ValueError('actual default storage ID digest differs: expected '+manifest['digest']+' observed '+str(direct.get('Digest'))[:128])
        with scratch(prefix='.local-check-',dir=module) as source_dir:
            source=Path(source_dir)/'main.go';source.write_text('package main\nimport("encoding/json";"os";"github.com/tuna-os/fisherman/internal/install")\nfunc main(){json.NewEncoder(os.Stdout).Encode(install.CheckImage(os.Args[1]))}\n')
            executable=folder/'check';run(['go','build','-o',str(executable),str(source)],'build-actual-cache-consumer',cwd=module)
            result=run([str(executable),source_ref],'actual-cache-consumer',env=env);check=json.loads(result.stdout.splitlines()[-1])
        if marker.exists() or check['NeedsPull'] or not check['Offline'] or check['LayerCount']!=1:raise ValueError('actual local-ID check queried registry or failed')
        if calls.read_text()!='inspect '+source_ref+'\n':raise ValueError('unexpected actual lookup count/source')
        RECEIPT.update(complete=True,scope='actual tiny owned-store image ID import/transport and compiled cache check',configId=identity,manifestDigest=manifest['digest'],observedPodmanDigest=observed[0]['Digest'],observedSkopeoDigest=direct['Digest'],actualCheckImage=check,registryLookupAttempted=False)

if __name__=='__main__':
    status=0
    try:bind();main()
    except Exception as error:
        status=1;RECEIPT.update(complete=False,failure=type(error).__name__,detail=str(error)[:4096])
    print(json.dumps(RECEIPT,sort_keys=True))
    raise SystemExit(status)

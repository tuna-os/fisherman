"""Bound owned native tool groups and observe wait/reap before scratch cleanup."""
import ctypes,hashlib,json,os,selectors,signal,subprocess,time
from pathlib import Path
RECEIPT={}
class CommandFailure(RuntimeError): pass

def rows(group):
    result=[]
    for path in Path('/proc').glob('[0-9]*/stat'):
        try:raw=path.read_text()
        except (FileNotFoundError,ProcessLookupError):continue
        values=raw.rsplit(')',1)[1].split()
        if int(values[2])==group and int(values[3])==group:
            result.append({'pid':int(path.parent.name),'startTicks':int(values[19]),'state':values[0]})
    return result

def reap_group(child):
    # The process starts a fresh session, and trusted native tools inherit it.
    # Become a subreaper so killed Go compiler children can also be waited for.
    try:os.killpg(child.pid,signal.SIGKILL)
    except ProcessLookupError:pass
    code=child.wait(timeout=5)
    deadline=time.monotonic()+5;reaped=[]
    while True:
        try:
            while True:
                pid,status=os.waitpid(-child.pid,os.WNOHANG)
                if not pid:break
                reaped.append({'pid':pid,'waitStatus':status})
        except ChildProcessError:pass
        remaining=rows(child.pid)
        if not remaining:return {'returnCode':code,'parentWaitObserved':True,'reapedDescendants':reaped,'groupEmptyObserved':True}
        if time.monotonic()>=deadline:raise CommandFailure('owned tool group not empty; refuse scratch cleanup')
        time.sleep(.01)

def run(argv,stage,env=None,cwd=None,timeout=60):
    if ctypes.CDLL(None,use_errno=True).prctl(36,1,0,0,0)!=0:raise OSError(ctypes.get_errno(),'subreaper unavailable')
    entry={'stage':stage,'command':list(map(str,argv)),'processStarted':False,'returnCode':None}
    RECEIPT.setdefault('commands',[]).append(entry)
    child=None;streams={};deadline=time.monotonic()+timeout
    try:
        child=subprocess.Popen(argv,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=env,cwd=cwd,start_new_session=True)
        entry.update(processStarted=True,pid=child.pid,session=child.pid)
        entry['initialGroupObservation']=rows(child.pid)
        streams={child.stdout:bytearray(),child.stderr:bytearray()}
        with selectors.DefaultSelector() as poll:
            for stream in streams:os.set_blocking(stream.fileno(),False);poll.register(stream,selectors.EVENT_READ)
            while poll.get_map():
                if time.monotonic()>=deadline:raise TimeoutError('owned command deadline spent')
                for key,_ in poll.select(min(.05,max(0,deadline-time.monotonic()))):
                    data=os.read(key.fileobj.fileno(),65536)
                    if not data:poll.unregister(key.fileobj);continue
                    streams[key.fileobj].extend(data)
                    if sum(map(len,streams.values()))>1048576:raise CommandFailure('owned command output exceeds 1 MiB')
        code=child.wait(timeout=max(.01,deadline-time.monotonic()));entry['returnCode']=code
        stdout=bytes(streams[child.stdout]);stderr=bytes(streams[child.stderr])
        if code:raise CommandFailure('native tool nonzero exit '+str(code))
        return subprocess.CompletedProcess(argv,code,stdout.decode('utf-8'),stderr.decode('utf-8',errors='replace'))
    except BaseException as error:
        entry['failureClass']=type(error).__name__
        raise
    finally:
        if child is not None:
            entry['reap']=reap_group(child);entry['returnCode']=entry['reap']['returnCode']
            for label,stream in (('stdout',child.stdout),('stderr',child.stderr)):
                raw=bytes(streams.get(stream,b''));entry[label]={'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest(),'preview':raw[:1024].decode('utf-8',errors='replace')}
                stream.close()

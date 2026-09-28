"""Stable, owner-only receipt reads from one frozen activation directory."""
from __future__ import annotations
import hashlib,json,math,os,re,stat
from pathlib import Path


def require(value,reason):
    if not value:raise ValueError(reason)


def directory(path):
    require(path.is_absolute() and path.resolve()==path and not any(p.is_symlink() for p in (path,*path.parents)),'receipt directory alias')
    info=path.stat();require(stat.S_ISDIR(info.st_mode) and info.st_uid==os.getuid() and stat.S_IMODE(info.st_mode)==0o700,'receipt directory unsafe')
    return (info.st_dev,info.st_ino,info.st_uid,info.st_mode)


def identity(info):
    return tuple(getattr(info,key) for key in ('st_dev','st_ino','st_uid','st_mode','st_nlink','st_size','st_mtime_ns','st_ctime_ns'))


class ReceiptStore:
    def __init__(self,state,*,activation_id=None,pending_publication=False):
        require(activation_id is None or re.fullmatch('[0-9a-f]{32}',activation_id),'invalid explicit activation')
        require(type(pending_publication) is bool,'invalid publication reader policy')
        self.pending_publication=pending_publication
        self.selected_activation=activation_id
        self.state=Path(state);self.state_identity=directory(self.state)
        self.state_fd=os.open(self.state,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
        info=os.fstat(self.state_fd)
        require((info.st_dev,info.st_ino,info.st_uid,info.st_mode)==self.state_identity,'receipt state changed during open')
        self.activation_id=None;self.activation_identity=None;self.activation_fd=None;self.pins={}

    def discover(self):
        require(directory(self.state)==self.state_identity,'receipt state replaced')
        names=os.listdir(self.state_fd)
        require(len(names)<=(2 if self.selected_activation else 1) and all(re.fullmatch('[0-9a-f]{32}',n) for n in names),'multiple or invalid service activation')
        if self.selected_activation is not None:names=[name for name in names if name==self.selected_activation]
        if not names:
            require(self.activation_id is None,'bound activation disappeared')
            return False
        name=names[0];path=self.state/name;current=directory(path)
        if self.activation_id is None:
            fd=os.open(name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=self.state_fd);info=os.fstat(fd)
            if (info.st_dev,info.st_ino,info.st_uid,info.st_mode)!=current:os.close(fd);raise ValueError('activation changed during open')
            self.activation_id=name;self.activation_identity=current;self.activation_fd=fd
        require(name==self.activation_id and current==self.activation_identity,'service activation replaced')
        return True

    def read(self,name):
        require(re.fullmatch(r'[a-z0-9-]+\.json',name) is not None,'receipt filename invalid')
        if not self.discover():return None
        try:fd=os.open(name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=self.activation_fd)
        except FileNotFoundError:return None
        try:
            before=os.fstat(fd)
            if self.pending_publication and before.st_nlink==2:
                require(stat.S_ISREG(before.st_mode) and before.st_uid==os.getuid() and stat.S_IMODE(before.st_mode)==0o600,'pending receipt unsafe')
                return None
            require(stat.S_ISREG(before.st_mode) and before.st_uid==os.getuid() and stat.S_IMODE(before.st_mode)==0o600 and before.st_nlink==1 and before.st_size<=4*1024*1024,'receipt file unsafe')
            chunks=[];left=before.st_size
            while left:
                chunk=os.read(fd,min(left,65536));require(chunk,'receipt short read');chunks.append(chunk);left-=len(chunk)
            require(not os.read(fd,1) and identity(before)==identity(os.fstat(fd)),'receipt changed while reading')
            raw=b''.join(chunks);digest=hashlib.sha256(raw).hexdigest()
            pin=(identity(before),digest)
            require(name not in self.pins or self.pins[name]==pin,'receipt replaced')
            self.pins[name]=pin
            def pairs(items):
                result={}
                for key,value in items:
                    require(key not in result,'duplicate receipt key');result[key]=value
                return result
            def constant(value):raise ValueError('nonfinite receipt number')
            def number(raw_number):
                value=float(raw_number);require(math.isfinite(value),'nonfinite receipt number');return value
            value=json.loads(raw,object_pairs_hook=pairs,parse_constant=constant,parse_float=number);require(isinstance(value,dict),'receipt shape invalid')
            self.discover();return value,digest
        finally:os.close(fd)

    def children(self):
        if not self.discover():return []
        return os.listdir(self.activation_fd)

    def close(self):
        if self.activation_fd is not None:os.close(self.activation_fd);self.activation_fd=None
        if self.state_fd is not None:os.close(self.state_fd);self.state_fd=None

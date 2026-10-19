"""Create a non-bootable test PE; sign separately with a disposable key."""
import sys
import struct
from pathlib import Path
sections=[('.text',b'\x31\xc0\xc3'),('.linux',b'not a bootable kernel\0'),('.osrel',b'ID=fisherman-test\n'),('.cmdline',b'quiet composefs=?' + b'a'*128 + b'\0'),('.pcrpkey',b'fixture-public-key\0'),('.sbat',b'sbat,1,SBAT Version,sbat,1,https://github.com/rhboot/shim\n')]
p=0x80; optional=p+24; table=optional+240; start=(table+40*len(sections)+511)//512*512
b=bytearray(start+512*len(sections))
def u16(o,v):struct.pack_into('<H',b,o,v)
def u32(o,v):struct.pack_into('<I',b,o,v)
b[:2]=b'MZ';u32(0x3c,p);b[p:p+4]=b'PE\0\0';u16(p+4,0x8664);u16(p+6,len(sections));u16(p+20,240);u16(p+22,0x2022)
u16(optional,0x20b);u32(optional+4,512);u32(optional+8,512*(len(sections)-1));u32(optional+16,0x1000);u32(optional+20,0x1000);struct.pack_into('<Q',b,optional+24,0x400000)
u32(optional+32,0x1000);u32(optional+36,512);u16(optional+40,2);u16(optional+48,2);u32(optional+56,(len(sections)+1)*0x1000);u32(optional+60,start);u16(optional+68,10);u32(optional+108,16)
for i,(name,data) in enumerate(sections):
 t=table+40*i; b[t:t+8]=name.encode().ljust(8,b'\0');u32(t+8,len(data));u32(t+12,0x1000*(i+1));u32(t+16,512);u32(t+20,start+512*i);u32(t+36,0x60000020 if i==0 else 0x40000040);b[start+512*i:start+512*i+len(data)]=data
Path(sys.argv[1]).write_bytes(b)

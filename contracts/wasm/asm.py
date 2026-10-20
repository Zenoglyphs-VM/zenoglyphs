import struct
def leb(n):
    out=bytearray()
    while True:
        b=n&0x7f; n>>=7
        if n: out.append(b|0x80)
        else: out.append(b); break
    return bytes(out)
def sleb(n):
    out=bytearray(); more=True
    while more:
        b=n&0x7f; n>>=7
        if (n==0 and not b&0x40) or (n==-1 and b&0x40): more=False
        else: b|=0x80
        out.append(b)
    return bytes(out)
def sec(i,p): return bytes([i])+leb(len(p))+p
def vec(x): return leb(len(x))+b"".join(x)
def nm(s): b=s.encode(); return leb(len(b))+b
HDR=b"\x00asm\x01\x00\x00\x00"
I32,F64,VOID = 0x7f,0x7c,0x40
def ftype(params,results): return bytes([0x60])+vec([bytes([p]) for p in params])+vec([bytes([r]) for r in results])
# instructions
def get(i): return b"\x20"+leb(i)
def setl(i): return b"\x21"+leb(i)
def i32c(n): return b"\x41"+sleb(n)
def f64c(x): return b"\x44"+struct.pack("<d",x)
ADD_I,SUB_I,MUL_I = b"\x6a",b"\x6b",b"\x6c"
GE_S,LT_S = b"\x4e",b"\x48"
ADD_F,SUB_F,MUL_F,DIV_F = b"\xa0",b"\xa1",b"\xa2",b"\xa3"
GT_F = b"\x64"
CONV = b"\xb7"                 # f64.convert_i32_s
def block(bt=VOID): return b"\x02"+bytes([bt])
def loop(bt=VOID):  return b"\x03"+bytes([bt])
END=b"\x0b"
def br(d): return b"\x0c"+leb(d)
def br_if(d): return b"\x0d"+leb(d)
def call(i): return b"\x10"+leb(i)
def store8(off): return b"\x3a\x00"+leb(off)
def locals(groups): return vec([leb(c)+bytes([t]) for c,t in groups])

# --- extras for the interactive renderer ---
DIV_S = b"\x6d"          # i32.div_s
EQ_I  = b"\x46"          # i32.eq
def iff(bt=VOID): return b"\x04"+bytes([bt])
ELSE = b"\x05"

# --- globals + misc for the stateful engine ---
def gget(i): return b"\x23"+leb(i)
def gset(i): return b"\x24"+leb(i)
def glob(vt, init, mut=1): return bytes([vt, mut])+init+END
LT_S_I = b"\x48"

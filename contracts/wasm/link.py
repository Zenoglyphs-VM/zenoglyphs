import os
def leb(n):
    out=bytearray()
    while True:
        b=n&0x7f; n>>=7
        if n: out.append(b|0x80)
        else: out.append(b); break
    return bytes(out)
def sec(sid,p): return bytes([sid])+leb(len(p))+p
def vec(items): return leb(len(items))+b"".join(items)
def name(s):
    b=s.encode(); return leb(len(b))+b
HDR=b"\x00asm\x01\x00\x00\x00"

# --- module B: imports lib.add, exports run() -> i32 ---
t_add = bytes([0x60])+vec([b"\x7f",b"\x7f"])+vec([b"\x7f"])   # (i32,i32)->i32
t_run = bytes([0x60])+vec([])+vec([b"\x7f"])                  # ()->i32
tsec = sec(1, vec([t_add, t_run]))
isec = sec(2, vec([name("lib")+name("add")+bytes([0x00])+leb(0)]))
fsec = sec(3, vec([b"\x01"]))                                  # local func, type 1
esec = sec(7, vec([name("run")+bytes([0x00])+leb(1)]))         # func idx 1 (0 = import)
body = vec([]) + bytes([0x41,20, 0x41,22, 0x10,0x00, 0x0b])    # const,const,call 0,end
csec = sec(10, vec([leb(len(body))+body]))
open("app.wasm","wb").write(HDR+tsec+isec+fsec+esec+csec)
print("app.wasm    %4d bytes  (imports lib.add)" % os.path.getsize("app.wasm"))
print("add.wasm    %4d bytes  (the shared library)" % os.path.getsize("add.wasm"))

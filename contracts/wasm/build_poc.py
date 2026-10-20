from asm import *
import os

# ============ lib.wasm — the reusable kernel ============
# iter(cx: f64, cy: f64, maxiter: i32) -> i32
# params: cx=0 cy=1 maxiter=2 | locals: zx=3 zy=4 t=5 (f64), i=6 (i32)
body = locals([(3,F64),(1,I32)]) + b"".join([
  f64c(0.0), setl(3), f64c(0.0), setl(4), i32c(0), setl(6),
  block(), loop(),
    get(6), get(2), GE_S, br_if(1),                      # i >= maxiter -> exit
    get(3),get(3),MUL_F, get(4),get(4),MUL_F, ADD_F,
      f64c(4.0), GT_F, br_if(1),                         # |z|^2 > 4 -> escaped
    get(3),get(3),MUL_F, get(4),get(4),MUL_F, SUB_F, get(0), ADD_F, setl(5),
    f64c(2.0), get(3), MUL_F, get(4), MUL_F, get(1), ADD_F, setl(4),
    get(5), setl(3),
    get(6), i32c(1), ADD_I, setl(6),
    br(0),
  END, END,
  get(6),
  END,
])
lib = (HDR
  + sec(1, vec([ftype([F64,F64,I32],[I32])]))
  + sec(3, vec([b"\x00"]))
  + sec(7, vec([nm("iter")+b"\x00"+leb(0)]))
  + sec(10, vec([leb(len(body))+body])))
open("lib.wasm","wb").write(lib)

# ============ app.wasm — imports lib.iter, renders into memory ============
# render(w,h,maxiter) ; params w=0 h=1 maxiter=2
# locals: x=3 y=4 it=5 p=6 (i32), cx=7 cy=8 (f64)
r = locals([(4,I32),(2,F64)]) + b"".join([
  i32c(0), setl(4),
  block(), loop(),
    get(4), get(1), GE_S, br_if(1),
    i32c(0), setl(3),
    block(), loop(),
      get(3), get(0), GE_S, br_if(1),
      get(3),CONV, get(0),CONV, DIV_F, f64c(3.5),MUL_F, f64c(2.5),SUB_F, setl(7),
      get(4),CONV, get(1),CONV, DIV_F, f64c(2.0),MUL_F, f64c(1.0),SUB_F, setl(8),
      get(7), get(8), get(2), call(0), setl(5),          # <-- the cross-module call
      get(4), get(0), MUL_I, get(3), ADD_I, i32c(4), MUL_I, setl(6),
      get(6), get(5), i32c(9),  MUL_I, store8(0),
      get(6), get(5), i32c(5),  MUL_I, store8(1),
      get(6), get(5), i32c(14), MUL_I, store8(2),
      get(6), i32c(255), store8(3),
      get(3), i32c(1), ADD_I, setl(3),
      br(0),
    END, END,
    get(4), i32c(1), ADD_I, setl(4),
    br(0),
  END, END,
  END,
])
app = (HDR
  + sec(1, vec([ftype([F64,F64,I32],[I32]), ftype([I32,I32,I32],[])]))
  + sec(2, vec([nm("lib")+nm("iter")+b"\x00"+leb(0)]))
  + sec(3, vec([b"\x01"]))
  + sec(5, vec([b"\x00"+leb(16)]))                        # 16 pages = 1 MiB
  + sec(7, vec([nm("render")+b"\x00"+leb(1), nm("mem")+b"\x02"+leb(0)]))
  + sec(10, vec([leb(len(r))+r])))
open("app.wasm","wb").write(app)

for f in ("lib.wasm","app.wasm"):
    print("%-9s %4d bytes" % (f, os.path.getsize(f)))

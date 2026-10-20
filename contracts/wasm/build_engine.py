from asm import *
import os
HOME_CX, HOME_CY, HOME_SPAN = -0.6, 0.0, 3.2

# globals: 0 w,1 h,2 maxiter (i32) | 3 cx,4 cy,5 span (f64) | 6 lastX,7 lastY,8 dragging (i32)
G = [glob(I32,i32c(600)), glob(I32,i32c(400)), glob(I32,i32c(255)),
     glob(F64,f64c(HOME_CX)), glob(F64,f64c(HOME_CY)), glob(F64,f64c(HOME_SPAN)),
     glob(I32,i32c(0)), glob(I32,i32c(0)), glob(I32,i32c(0))]

W,H,IT,CX,CY,SPAN,LX,LY,DRAG = range(9)
F_ITER,F_SCALE,F_PX,F_PY,F_RENDER,F_INIT,F_DOWN,F_MOVE,F_UP,F_ZOOM,F_SETIT,F_RESET,F_PROBE,F_GCX,F_GCY,F_GZOOM = range(16)

def fn(loc, body): b = locals(loc)+b"".join(body)+END; return leb(len(b))+b

# scale() -> f64 : span / w
scale = fn([], [gget(SPAN), gget(W), CONV, DIV_F])
# pxToCx(px) -> f64 : cx + (px - w/2)*scale
px2cx = fn([], [gget(CX), get(0), gget(W), i32c(2), DIV_S, SUB_I, CONV, call(F_SCALE), MUL_F, ADD_F])
py2cy = fn([], [gget(CY), get(0), gget(H), i32c(2), DIV_S, SUB_I, CONV, call(F_SCALE), MUL_F, ADD_F])

# render() : locals x=0 y=1 it=2 p=3 (i32), s=4 cxv=5 cyv=6 (f64)
render = fn([(4,I32),(3,F64)], [
  call(F_SCALE), setl(4),
  i32c(0), setl(1),
  block(), loop(),
    get(1), gget(H), GE_S, br_if(1),
    gget(CY), get(1), gget(H), i32c(2), DIV_S, SUB_I, CONV, get(4), MUL_F, ADD_F, setl(6),
    i32c(0), setl(0),
    block(), loop(),
      get(0), gget(W), GE_S, br_if(1),
      gget(CX), get(0), gget(W), i32c(2), DIV_S, SUB_I, CONV, get(4), MUL_F, ADD_F, setl(5),
      get(5), get(6), gget(IT), call(F_ITER), setl(2),
      get(1), gget(W), MUL_I, get(0), ADD_I, i32c(4), MUL_I, setl(3),
      get(2), gget(IT), EQ_I,
      iff(),
        get(3), i32c(0), store8(0), get(3), i32c(0), store8(1), get(3), i32c(0), store8(2),
      ELSE,
        get(3), get(2), i32c(9),  MUL_I, store8(0),
        get(3), get(2), i32c(5),  MUL_I, store8(1),
        get(3), get(2), i32c(14), MUL_I, store8(2),
      END,
      get(3), i32c(255), store8(3),
      get(0), i32c(1), ADD_I, setl(0), br(0),
    END, END,
    get(1), i32c(1), ADD_I, setl(1), br(0),
  END, END,
])
init   = fn([], [get(0), gset(W), get(1), gset(H)])
down   = fn([], [get(0), gset(LX), get(1), gset(LY), i32c(1), gset(DRAG)])
up     = fn([], [i32c(0), gset(DRAG)])
# move(x,y): if dragging { cx -= (x-lastX)*scale; cy -= (y-lastY)*scale; last = xy }
move   = fn([], [
  gget(DRAG), iff(),
    gget(CX), get(0), gget(LX), SUB_I, CONV, call(F_SCALE), MUL_F, SUB_F, gset(CX),
    gget(CY), get(1), gget(LY), SUB_I, CONV, call(F_SCALE), MUL_F, SUB_F, gset(CY),
    get(0), gset(LX), get(1), gset(LY),
  END])
# zoom(px,py,dir): keep the point under the cursor fixed
zoom = fn([(2,F64)], [
  get(0), call(F_PX), setl(3),
  get(1), call(F_PY), setl(4),
  get(2), i32c(0), LT_S_I,
  iff(), gget(SPAN), f64c(0.8),  MUL_F, gset(SPAN),
  ELSE,  gget(SPAN), f64c(1.25), MUL_F, gset(SPAN), END,
  get(3), get(0), gget(W), i32c(2), DIV_S, SUB_I, CONV, call(F_SCALE), MUL_F, SUB_F, gset(CX),
  get(4), get(1), gget(H), i32c(2), DIV_S, SUB_I, CONV, call(F_SCALE), MUL_F, SUB_F, gset(CY),
])
setit  = fn([], [get(0), gset(IT)])
reset  = fn([], [f64c(HOME_CX), gset(CX), f64c(HOME_CY), gset(CY), f64c(HOME_SPAN), gset(SPAN)])
probe  = fn([], [get(0), call(F_PX), get(1), call(F_PY), gget(IT), call(F_ITER)])
gcx    = fn([], [gget(CX)])
gcy    = fn([], [gget(CY)])
gzoom  = fn([], [f64c(HOME_SPAN), gget(SPAN), DIV_F])

types = [ftype([F64,F64,I32],[I32]), ftype([],[F64]), ftype([I32],[F64]), ftype([],[]),
         ftype([I32,I32],[]), ftype([I32,I32,I32],[]), ftype([I32],[]), ftype([I32,I32],[I32])]
sig   = [1,2,2,3,4,4,4,3,5,6,3,7,1,1,1]      # for local funcs 1..15
bodies= [scale,px2cx,py2cy,render,init,down,move,up,zoom,setit,reset,probe,gcx,gcy,gzoom]
exports = [("mem",0x02,0),("render",0,F_RENDER),("init",0,F_INIT),("down",0,F_DOWN),
           ("move",0,F_MOVE),("up",0,F_UP),("zoom",0,F_ZOOM),("setIter",0,F_SETIT),
           ("reset",0,F_RESET),("probe",0,F_PROBE),("cx",0,F_GCX),("cy",0,F_GCY),("zoomLevel",0,F_GZOOM)]

mod = (HDR
  + sec(1, vec([t for t in types]))
  + sec(2, vec([nm("lib")+nm("iter")+b"\x00"+leb(0)]))
  + sec(3, vec([leb(s) for s in sig]))
  + sec(5, vec([b"\x00"+leb(32)]))
  + sec(6, vec(G))
  + sec(7, vec([nm(n)+bytes([k])+leb(i) for n,k,i in exports]))
  + sec(10, vec(bodies)))
open("engine.wasm","wb").write(mod)
print("engine.wasm %4d bytes · %d exported functions" % (os.path.getsize("engine.wasm"), len(exports)-1))

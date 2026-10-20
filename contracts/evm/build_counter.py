from asm import Asm, deployer
import hashlib, sys

# keccak-256 via pysha3 is unavailable; shell out to the Go helper for topics.
TOPIC = sys.argv[1] if len(sys.argv)>1 else "00"*32

INC, GET, ADD = 0xd09de08a, 0x6d4ce63c, 0x1003e2d2

a = Asm()
# selector = calldata[0:4]
a.push(0).op('CALLDATALOAD').push(0xE0).op('SHR')
# dispatch table
for sel, lbl in ((INC,'inc'), (GET,'get'), (ADD,'addv')):
    a.op('DUP1'); a.push(sel,4); a.op('EQ'); a.pushlabel(lbl); a.op('JUMPI')
# unknown selector -> revert
a.push(0).push(0).op('REVERT')

a.label('inc').op('JUMPDEST')
a.push(0).op('SLOAD').push(1).op('ADD')          # new = old + 1
a.op('DUP1').push(0).op('SSTORE')                # persist
a.push(0).op('MSTORE')                           # mem[0] = new
a.push(TOPIC,32).push(0x20).push(0).op('LOG1')   # emit Incremented(new)
a.push(0x20).push(0).op('RETURN')

a.label('get').op('JUMPDEST')
a.push(0).op('SLOAD').push(0).op('MSTORE')
a.push(0x20).push(0).op('RETURN')

a.label('addv').op('JUMPDEST')
a.push(4).op('CALLDATALOAD')                     # the uint256 argument
a.push(0).op('SLOAD').op('ADD')
a.op('DUP1').push(0).op('SSTORE')
a.push(0).op('MSTORE')
a.push(0x20).push(0).op('RETURN')

runtime = a.assemble()
init = deployer(runtime)
open("counter.runtime.bin","wb").write(runtime)
open("counter.bin","wb").write(init)
print(f"runtime {len(runtime)} bytes   init {len(init)} bytes")
print("runtime hex:", runtime.hex())
print("labels:", a.labels)

from asm import Asm

TRANSFER_TOPIC = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
APPROVAL_TOPIC = "8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"
ADDR_MASK      = "ff"*20
SEL = dict(totalSupply=0x18160ddd, balanceOf=0x70a08231, transfer=0xa9059cbb,
           approve=0x095ea7b3, allowance=0xdd62ed3e, transferFrom=0x23b872dd,
           decimals=0x313ce567)

def mask(a):                       # keep the low 20 bytes of a word
    a.push(ADDR_MASK,20); a.op('AND'); return a

def mapslot(a, slot):              # key on stack -> keccak(key . slot)
    a.push(0).op('MSTORE')
    a.push(slot).push(0x20).op('MSTORE')
    a.push(0x40).push(0).op('KECCAK256'); return a

def ret_word(a):                   # value on stack -> return it as 32 bytes
    a.push(0).op('MSTORE'); a.push(0x20).push(0).op('RETURN'); return a

def build(total_supply: int):
    a = Asm()
    # ---- dispatch on the 4-byte selector -------------------------------
    a.push(0).op('CALLDATALOAD').push(0xE0).op('SHR')
    for name in ('totalSupply','balanceOf','transfer','approve',
                 'allowance','transferFrom','decimals'):
        a.op('DUP1'); a.push(SEL[name],4); a.op('EQ'); a.pushlabel(name); a.op('JUMPI')
    a.label('bad').push(0).push(0).op('REVERT')

    # ---- totalSupply() -------------------------------------------------
    a.label('totalSupply').op('JUMPDEST')
    a.push(0).op('SLOAD'); ret_word(a)

    # ---- decimals() ----------------------------------------------------
    a.label('decimals').op('JUMPDEST')
    a.push(18); ret_word(a)

    # ---- balanceOf(address) --------------------------------------------
    a.label('balanceOf').op('JUMPDEST')
    a.push(0x04).op('CALLDATALOAD'); mask(a); mapslot(a,1)
    a.op('SLOAD'); ret_word(a)

    # ---- transfer(address,uint256) -------------------------------------
    a.label('transfer').op('JUMPDEST')
    a.push(0x24).op('CALLDATALOAD')                    # amt
    a.push(0x04).op('CALLDATALOAD'); mask(a)           # amt, to
    a.op('CALLER'); mapslot(a,1)                       # amt, to, fromSlot
    a.op('DUP1').op('SLOAD').op('DUP4').op('GT')       # amt > fromBal ?
    a.pushlabel('bad'); a.op('JUMPI')
    a.op('DUP1').op('SLOAD').op('DUP4').op('SWAP1').op('SUB')   # fromBal-amt
    a.op('SWAP1').op('SSTORE')                         # amt, to
    a.op('DUP1'); mapslot(a,1)                         # amt, to, toSlot
    a.op('DUP1').op('SLOAD').op('DUP4').op('ADD')      # toBal+amt
    a.op('SWAP1').op('SSTORE')                         # amt, to
    a.op('DUP2').push(0).op('MSTORE')                  # mem[0]=amt
    a.op('DUP1').op('CALLER').push(TRANSFER_TOPIC,32)  # t2=to, t1=from, t0
    a.push(0x20).push(0).op('LOG3')
    a.push(1); ret_word(a)

    # ---- approve(address,uint256) --------------------------------------
    a.label('approve').op('JUMPDEST')
    a.push(0x24).op('CALLDATALOAD')                    # amt
    a.push(0x04).op('CALLDATALOAD'); mask(a)           # amt, spender
    a.op('CALLER'); mapslot(a,2)                       # amt, spender, inner
    a.push(0x20).op('MSTORE')                          # mem[32]=inner
    a.op('DUP1').push(0).op('MSTORE')                  # mem[0]=spender
    a.push(0x40).push(0).op('KECCAK256')               # amt, spender, allowSlot
    a.op('DUP3').op('SWAP1').op('SSTORE')              # amt, spender
    a.op('DUP2').push(0).op('MSTORE')                  # mem[0]=amt
    a.op('DUP1').op('CALLER').push(APPROVAL_TOPIC,32)
    a.push(0x20).push(0).op('LOG3')
    a.push(1); ret_word(a)

    # ---- allowance(address,address) ------------------------------------
    a.label('allowance').op('JUMPDEST')
    a.push(0x04).op('CALLDATALOAD'); mask(a); mapslot(a,2)
    a.push(0x20).op('MSTORE')
    a.push(0x24).op('CALLDATALOAD'); mask(a)
    a.push(0).op('MSTORE')
    a.push(0x40).push(0).op('KECCAK256')
    a.op('SLOAD'); ret_word(a)

    # ---- transferFrom(address,address,uint256) -------------------------
    a.label('transferFrom').op('JUMPDEST')
    a.push(0x44).op('CALLDATALOAD')                    # amt
    a.push(0x24).op('CALLDATALOAD'); mask(a)           # amt, to
    a.push(0x04).op('CALLDATALOAD'); mask(a)           # amt, to, from
    a.op('DUP1'); mapslot(a,2)                         # amt,to,from, inner
    a.push(0x20).op('MSTORE')
    a.op('CALLER').push(0).op('MSTORE')
    a.push(0x40).push(0).op('KECCAK256')               # amt,to,from, allowSlot
    a.op('DUP1').op('SLOAD').op('DUP5').op('GT')       # amt > allowed ?
    a.pushlabel('bad'); a.op('JUMPI')
    a.op('DUP1').op('SLOAD').op('DUP5').op('SWAP1').op('SUB')
    a.op('SWAP1').op('SSTORE')                         # amt,to,from
    a.op('DUP1'); mapslot(a,1)                         # amt,to,from, fromSlot
    a.op('DUP1').op('SLOAD').op('DUP5').op('GT')       # amt > fromBal ?
    a.pushlabel('bad'); a.op('JUMPI')
    a.op('DUP1').op('SLOAD').op('DUP5').op('SWAP1').op('SUB')
    a.op('SWAP1').op('SSTORE')                         # amt,to,from
    a.op('SWAP1')                                      # amt,from,to
    a.op('DUP1'); mapslot(a,1)                         # amt,from,to, toSlot
    a.op('DUP1').op('SLOAD').op('DUP5').op('ADD')
    a.op('SWAP1').op('SSTORE')                         # amt,from,to
    a.op('DUP3').push(0).op('MSTORE')                  # mem[0]=amt
    a.op('DUP1').op('DUP3').push(TRANSFER_TOPIC,32)    # t2=to, t1=from, t0
    a.push(0x20).push(0).op('LOG3')
    a.push(1); ret_word(a)

    runtime = a.assemble()

    # ---- constructor: mint the whole supply to the deployer ------------
    def ctor(off):
        c = Asm()
        c.push(total_supply,32)
        c.op('DUP1').push(0).op('SSTORE')              # totalSupply = supply
        c.op('CALLER'); mapslot(c,1); c.op('SSTORE')   # balances[deployer] = supply
        c.push(len(runtime),2).op('DUP1').push(off,2).push(0).op('CODECOPY')
        c.push(0).op('RETURN')
        return c.assemble()
    off = len(ctor(0))
    init = ctor(off) + runtime
    assert len(ctor(off)) == off
    return init, runtime, a.labels

if __name__ == '__main__':
    SUPPLY = 1_000_000 * 10**18
    init, runtime, labels = build(SUPPLY)
    open('erc20.bin','wb').write(init)
    open('erc20.runtime.bin','wb').write(runtime)
    print(f"runtime {len(runtime)} bytes  init {len(init)} bytes  supply {SUPPLY}")
    print("entry points:", {k:hex(v) for k,v in labels.items()})

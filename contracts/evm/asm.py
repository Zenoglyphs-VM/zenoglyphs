"""A tiny EVM assembler: labels, PUSH sizing, and init-code wrapping."""
OPS = {n:v for n,v in {
 'STOP':0x00,'ADD':0x01,'MUL':0x02,'SUB':0x03,'DIV':0x04,'SDIV':0x05,'MOD':0x06,
 'SMOD':0x07,'ADDMOD':0x08,'MULMOD':0x09,'EXP':0x0a,'SIGNEXTEND':0x0b,
 'LT':0x10,'GT':0x11,'SLT':0x12,'SGT':0x13,'EQ':0x14,'ISZERO':0x15,'AND':0x16,
 'OR':0x17,'XOR':0x18,'NOT':0x19,'BYTE':0x1a,'SHL':0x1b,'SHR':0x1c,'SAR':0x1d,
 'KECCAK256':0x20,'ADDRESS':0x30,'BALANCE':0x31,'ORIGIN':0x32,'CALLER':0x33,
 'CALLVALUE':0x34,'CALLDATALOAD':0x35,'CALLDATASIZE':0x36,'CALLDATACOPY':0x37,
 'CODESIZE':0x38,'CODECOPY':0x39,'EXTCODESIZE':0x3b,'RETURNDATASIZE':0x3d,
 'RETURNDATACOPY':0x3e,'TIMESTAMP':0x42,'NUMBER':0x43,'CHAINID':0x46,
 'SELFBALANCE':0x47,'POP':0x50,'MLOAD':0x51,'MSTORE':0x52,'MSTORE8':0x53,
 'SLOAD':0x54,'SSTORE':0x55,'JUMP':0x56,'JUMPI':0x57,'PC':0x58,'MSIZE':0x59,
 'GAS':0x5a,'JUMPDEST':0x5b,'PUSH0':0x5f,'CALL':0xf1,'RETURN':0xf3,
 'STATICCALL':0xfa,'REVERT':0xfd,'INVALID':0xfe,
}.items()}
for i in range(1,17):
    OPS[f'DUP{i}']=0x80+i-1; OPS[f'SWAP{i}']=0x90+i-1
for i in range(0,5): OPS[f'LOG{i}']=0xa0+i

class Asm:
    def __init__(self): self.items=[]      # ('op',b) ('push',val,size) ('label',n) ('pushlabel',n)
    def op(self,name): self.items.append(('op',OPS[name])); return self
    def push(self,v,size=None):
        if isinstance(v,str): v=int(v,16)   # hex, with or without 0x
        if size is None: size = max(1,(v.bit_length()+7)//8)
        b = v.to_bytes(size,'big')
        self.items.append(('push',b)); return self
    def label(self,n): self.items.append(('label',n)); return self
    def pushlabel(self,n): self.items.append(('pushlabel',n)); return self
    def __getattr__(self,name):
        if name.upper() in OPS: return lambda: self.op(name.upper())
        raise AttributeError(name)
    def assemble(self):
        # two passes: labels are always PUSH2 so sizes never shift
        for _ in range(2):
            pos, labels = 0, {}
            for it in self.items:
                if it[0]=='label': labels[it[1]]=pos
                elif it[0]=='op': pos+=1
                elif it[0]=='push': pos+=1+len(it[1])
                elif it[0]=='pushlabel': pos+=3
            self.labels=labels
        out=bytearray()
        for it in self.items:
            if it[0]=='label': pass
            elif it[0]=='op': out.append(it[1])
            elif it[0]=='push': out.append(0x60+len(it[1])-1); out+=it[1]
            elif it[0]=='pushlabel': out.append(0x61); out+=self.labels[it[1]].to_bytes(2,'big')
        return bytes(out)

def deployer(runtime: bytes) -> bytes:
    """Init code that returns `runtime` as the deployed code."""
    a=Asm()
    a.push(len(runtime),2)          # size
    a.op('DUP1')
    a.push(0,2)                     # offset placeholder, patched below
    a.push(0)                       # dest in memory
    a.op('CODECOPY')
    a.push(0)
    a.op('RETURN')
    head=a.assemble()
    # the runtime sits immediately after the init code; patch its offset in
    off=len(head)
    a2=Asm()
    a2.push(len(runtime),2); a2.op('DUP1'); a2.push(off,2); a2.push(0)
    a2.op('CODECOPY'); a2.push(0); a2.op('RETURN')
    head2=a2.assemble()
    assert len(head2)==off, (len(head2), off)
    return head2+runtime

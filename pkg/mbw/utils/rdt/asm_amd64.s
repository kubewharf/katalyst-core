//+build amd64,!gccgo,!noasm,!appengine

// func asmCpuidex(op, op2 uint32) (eax, ebx, ecx, edx uint32)
TEXT ·asmCpuidex(SB), 7, $0
	MOVL op+0(FP), AX
	MOVL op2+4(FP), CX
	CPUID
	MOVL AX, eax+8(FP)
	MOVL BX, ebx+12(FP)
	MOVL CX, ecx+16(FP)
	MOVL DX, edx+20(FP)
	RET

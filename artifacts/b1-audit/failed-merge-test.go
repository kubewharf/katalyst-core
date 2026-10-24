// Failed B1 test: TestP0B1TwoRoundIdempotent8CPU
// Topology: GenerateDummyCPUTopology(8, 1, 2) → 8 CPUs, 1 socket, 2 NUMA
// NUMA 0: CPUs 0-3, NUMA 1: CPUs 4-7
// State: dedicated-pod (exclusive) + reclaim C

C := machine.NewCPUSet(0, 1)          // reclaim C = NUMA 0 CPUs 0,1
D := machine.NewCPUSet(2, 3, 6, 7)    // dedicated D = NUMA 0 (2,3) + NUMA 1 (6,7)

// State entries:
//   dedicated-pod/main: OwnerPoolName=dedicated, QoSLevel=dedicated,
//     Annotations: numaBinding=enable, numaExclusive=enable
//     AllocationResult=D, ContainerType=MAIN
//   reclaim/faked: AllocationResult=C

// Response attempted (v1):
resp := &advisorapi.ListAndWatchResponse{
    Entries: map[string]*advisorapi.CalculationEntries{
        "dedicated-pod": {
            Entries: map[string]*advisorapi.CalculationInfo{
                "main": {
                    OwnerPoolName: commonstate.PoolNameDedicated,
                    CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
                        0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated", Result: uint64(D.Size())}}},
                    },
                },
            },
        },
    },
}
// Error v1: numaQuantity: 6 exceeds NUMA capacity: 4 in NUMA: 0

// Response attempted (v2):
resp := &advisorapi.ListAndWatchResponse{
    Entries: map[string]*advisorapi.CalculationEntries{
        "dedicated-pod": {
            Entries: map[string]*advisorapi.CalculationInfo{
                "main": {
                    OwnerPoolName: commonstate.PoolNameDedicated,
                    CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
                        0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-0", Result: 2}}},
                        1: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-1", Result: 2}}},
                    },
                },
            },
        },
    },
}
// Error v2: prepare applyBlocks failed: block dedicated-0 not found, entry: dedicated-pod, subEntry: main

// Root cause: block generator produces empty blockCPUSet when all CPUs
// are already allocated in state (no remaining CPUs to assign to new blocks)

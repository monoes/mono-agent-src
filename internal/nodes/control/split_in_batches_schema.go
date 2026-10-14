package control

// SplitInBatchesNodeSchema documents the config keys
// SplitInBatchesNode.Execute reads out of its map[string]interface{}
// config — see SetNodeSchema's doc comment for why this is a companion
// struct rather than the runtime config, and internal/tools/schemagen for
// the tag grammar.
//
// Execute enforces batch_size in [1, 1000]. "reset" is read again now that
// the node keeps loop state between runs (it was dead UI before).
type SplitInBatchesNodeSchema struct {
	BatchSize float64 `json:"batch_size" schema:"label=Batch Size,type=number,required,default=10,min=1,max=1000,help=Loop Over Items: the loop output sends one batch per iteration; wire the end of the loop body back into this node to continue. The done output sends all items once after the last batch."`
	Reset     bool    `json:"reset" schema:"label=Reset,type=boolean,default=false,help=Treat this run's input as a new set instead of continuing the current loop (e.g. paging until a page comes back empty)."`
}

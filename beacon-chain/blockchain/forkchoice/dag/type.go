package dag

// Node ...
type Node struct {
	// Parent ...
	Parent *Node
	// Children ...
	Children []*Node
	// Slot ...
	Slot uint64
	// Weight ...
	Weight uint64
	// Key ...
	Key [32]byte
	// IndexAsChild ...
	IndexAsChild uint64
}

// ScoreChange ...
type ScoreChange struct {
	// Target ...
	Target *Node
	// Delta ...
	Delta uint64
}

// ChildScore ...
type ChildScore struct {
	// BestTarget
	BestTarget *Node
	// Score
	Score uint64
}

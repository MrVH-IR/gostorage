package chunk

import "fmt"

type Plan struct {
	Index        int
	Offset, Size int64
}

func PlanFile(size int64, nodes int) ([]Plan, error) {
	if size <= 0 {
		return nil, fmt.Errorf("file size must be positive")
	}
	if nodes < 2 {
		return nil, fmt.Errorf("at least two storage nodes are required")
	}
	if int64(nodes) > size {
		return nil, fmt.Errorf("node count cannot exceed file size in bytes")
	}
	base, rem := size/int64(nodes), size%int64(nodes)
	out := make([]Plan, nodes)
	var offset int64
	for i := range out {
		n := base
		if int64(i) < rem {
			n++
		}
		out[i] = Plan{Index: i, Offset: offset, Size: n}
		offset += n
	}
	return out, nil
}

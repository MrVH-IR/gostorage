package chunk

import "testing"

func TestPlanFile(t *testing.T) {
	cases := []struct {
		s    int64
		n    int
		want []Plan
	}{{10, 3, []Plan{{0, 0, 4}, {1, 4, 3}, {2, 7, 3}}}, {8, 2, []Plan{{0, 0, 4}, {1, 4, 4}}}}
	for _, c := range cases {
		got, e := PlanFile(c.s, c.n)
		if e != nil {
			t.Fatal(e)
		}
		if len(got) != len(c.want) {
			t.Fatalf("got %#v", got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("got %#v want %#v", got, c.want)
			}
		}
	}
	for _, c := range []struct {
		s int64
		n int
	}{{0, 2}, {1, 2}, {2, 1}} {
		if _, e := PlanFile(c.s, c.n); e == nil {
			t.Fatalf("expected error for %d/%d", c.s, c.n)
		}
	}
}

func TestPlanFileRangesAreContiguous(t *testing.T) {
	for size := int64(2); size <= 256; size++ {
		for nodes := 2; int64(nodes) <= size && nodes <= 32; nodes++ {
			plans, err := PlanFile(size, nodes)
			if err != nil {
				t.Fatalf("size=%d nodes=%d: %v", size, nodes, err)
			}
			var offset, total int64
			for i, p := range plans {
				if p.Index != i || p.Offset != offset || p.Size <= 0 {
					t.Fatalf("invalid plan for size=%d nodes=%d: %+v", size, nodes, p)
				}
				offset += p.Size
				total += p.Size
			}
			if total != size || offset != size {
				t.Fatalf("size=%d nodes=%d totals %d/%d", size, nodes, total, offset)
			}
		}
	}
}

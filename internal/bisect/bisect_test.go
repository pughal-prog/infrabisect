package bisect

import (
	"context"
	"errors"
	"testing"

	"github.com/pughalan/infrabisect/internal/model"
)

// oracle returns a tester where states >= culprit are bad and states in
// skip are untestable.
func oracle(culprit int, skip map[int]bool, calls *int) Tester {
	return func(_ context.Context, k int) (model.Verdict, error) {
		*calls++
		if skip[k] {
			return model.Skip, nil
		}
		if k >= culprit {
			return model.Bad, nil
		}
		return model.Good, nil
	}
}

func TestSearchFindsEveryCulprit(t *testing.T) {
	for n := 1; n <= 64; n++ {
		for culprit := 1; culprit <= n; culprit++ {
			calls := 0
			res, err := Search(context.Background(), n, oracle(culprit, nil, &calls), Options{VerifyBad: true})
			if err != nil {
				t.Fatalf("n=%d culprit=%d: %v", n, culprit, err)
			}
			if res.FirstBad != culprit {
				t.Fatalf("n=%d culprit=%d: got %d", n, culprit, res.FirstBad)
			}
			if limit := MaxSteps(n) + 1; calls > limit {
				t.Fatalf("n=%d culprit=%d: %d calls, want <= %d", n, culprit, calls, limit)
			}
		}
	}
}

func TestSearchRoutesAroundSkips(t *testing.T) {
	calls := 0
	// State 12 is untestable but not adjacent to the boundary, so the search
	// probes a neighbour instead and still isolates change 9.
	res, err := Search(context.Background(), 16, oracle(9, map[int]bool{12: true}, &calls), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.FirstBad != 9 {
		t.Fatalf("got %d (candidates %v), want 9", res.FirstBad, res.Candidates)
	}
	skipped := false
	for _, p := range res.Tested {
		skipped = skipped || (p.State == 12 && p.Verdict == model.Skip)
	}
	if !skipped {
		t.Fatalf("state 12 was never probed: %+v", res.Tested)
	}
}

func TestSearchInconclusiveWhenNeighboursSkipped(t *testing.T) {
	calls := 0
	res, err := Search(context.Background(), 10, oracle(6, map[int]bool{5: true}, &calls), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.FirstBad != 0 {
		t.Fatalf("expected inconclusive, got culprit %d", res.FirstBad)
	}
	if len(res.Candidates) != 2 || res.Candidates[0] != 5 || res.Candidates[1] != 6 {
		t.Fatalf("candidates = %v, want [5 6]", res.Candidates)
	}
}

func TestSearchVerification(t *testing.T) {
	calls := 0
	_, err := Search(context.Background(), 5, oracle(99, nil, &calls), Options{VerifyBad: true})
	if !errors.Is(err, ErrBadIsGood) {
		t.Fatalf("err = %v, want ErrBadIsGood", err)
	}
	_, err = Search(context.Background(), 5, oracle(0, nil, &calls), Options{VerifyGood: true})
	if !errors.Is(err, ErrBaselineIsBad) {
		t.Fatalf("err = %v, want ErrBaselineIsBad", err)
	}
	if _, err := Search(context.Background(), 0, oracle(1, nil, &calls), Options{}); !errors.Is(err, ErrNoChanges) {
		t.Fatalf("err = %v, want ErrNoChanges", err)
	}
}

func TestSearchStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if _, err := Search(ctx, 8, oracle(3, nil, &calls), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("tester called %d times after cancel", calls)
	}
}

func TestMaxSteps(t *testing.T) {
	for n, want := range map[int]int{0: 0, 1: 0, 2: 1, 3: 2, 4: 2, 5: 3, 8: 3, 9: 4, 1000: 10} {
		if got := MaxSteps(n); got != want {
			t.Errorf("MaxSteps(%d) = %d, want %d", n, got, want)
		}
	}
}

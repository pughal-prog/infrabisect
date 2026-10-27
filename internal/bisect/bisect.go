// Package bisect implements binary search for the first bad state on a
// timeline of N changes, with skip support in the style of git bisect.
package bisect

import (
	"context"
	"errors"
	"fmt"
	"math/bits"

	"github.com/pughal-prog/infrabisect/internal/model"
)

// Tester evaluates state k (0 = baseline, n = present).
type Tester func(ctx context.Context, k int) (model.Verdict, error)

// Options controls endpoint verification.
type Options struct {
	VerifyGood bool // test state 0 before searching
	VerifyBad  bool // test state n before searching
}

// Result describes the outcome of a search.
type Result struct {
	// FirstBad is the culprit state (change FirstBad introduced the
	// failure); 0 when the search was inconclusive.
	FirstBad int
	// Candidates lists the states that may hold the culprit when skipped
	// states prevented narrowing to one.
	Candidates []int
	// Tested records verdicts in the order states were evaluated.
	Tested []Probe
}

// Probe is one evaluated state.
type Probe struct {
	State   int
	Verdict model.Verdict
}

var (
	ErrNoChanges     = errors.New("no changes between the good and bad versions: nothing to bisect")
	ErrBadIsGood     = errors.New("the bad state passes all checks: the failure does not reproduce")
	ErrBaselineIsBad = errors.New("the good baseline fails the checks: pick an earlier good version")
)

// MaxSteps estimates how many states a search over n changes tests,
// excluding endpoint verification.
func MaxSteps(n int) int {
	if n <= 1 {
		return 0
	}
	return bits.Len(uint(n - 1))
}

// Search finds the first bad state in (0, n]. State 0 is assumed good and
// state n bad unless verified via opts.
func Search(ctx context.Context, n int, test Tester, opts Options) (*Result, error) {
	if n <= 0 {
		return nil, ErrNoChanges
	}
	res := &Result{}
	probe := func(k int) (model.Verdict, error) {
		if err := ctx.Err(); err != nil {
			return model.Skip, err
		}
		v, err := test(ctx, k)
		if err != nil {
			return v, err
		}
		res.Tested = append(res.Tested, Probe{State: k, Verdict: v})
		return v, nil
	}

	if opts.VerifyBad {
		v, err := probe(n)
		if err != nil {
			return res, err
		}
		switch v {
		case model.Good:
			return res, ErrBadIsGood
		case model.Skip:
			return res, fmt.Errorf("the bad state (%d) could not be tested", n)
		}
	}
	if opts.VerifyGood {
		v, err := probe(0)
		if err != nil {
			return res, err
		}
		switch v {
		case model.Bad:
			return res, ErrBaselineIsBad
		case model.Skip:
			return res, errors.New("the good baseline could not be tested")
		}
	}

	lo, hi := 0, n
	skipped := map[int]bool{}
	for {
		k := pick(lo, hi, skipped)
		if k < 0 {
			break
		}
		v, err := probe(k)
		if err != nil {
			return res, err
		}
		switch v {
		case model.Good:
			lo = k
		case model.Bad:
			hi = k
		default:
			skipped[k] = true
		}
	}
	if hi-lo == 1 {
		res.FirstBad = hi
		return res, nil
	}
	for k := lo + 1; k <= hi; k++ {
		res.Candidates = append(res.Candidates, k)
	}
	return res, nil
}

// pick returns the untested, unskipped state closest to the middle of the
// open interval (lo, hi), or -1 when none remains.
func pick(lo, hi int, skipped map[int]bool) int {
	mid := lo + (hi-lo)/2
	for d := 0; mid-d > lo || mid+d < hi; d++ {
		if k := mid - d; k > lo && !skipped[k] {
			return k
		}
		if k := mid + d; d > 0 && k < hi && !skipped[k] {
			return k
		}
	}
	return -1
}

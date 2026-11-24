package app

import (
	"fmt"
	"sync"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestStyleNeverChangesAGuardrail (R28) plays the sequence property test (PLAN 4.12) in a
// reduced run for every risk preset × aggressiveness × autonomy level: every invariant of
// runPropScenario (the two order shapes, the request id stored before the send, cash, budget
// and short caps, stops on the losing side, nothing after a kill, a drawdown or the daily
// latch, flat before the close, no resend) holds at every combination, although bolder
// levels propose larger entries. The agent uses the chosen aggressiveness, and at low
// autonomy it never trades a share the scanner added.
func TestStyleNeverChangesAGuardrail(t *testing.T) {
	const ticks = 60
	var (
		mu      sync.Mutex
		total   propStats
		entries = map[domain.Aggressiveness]int{}
	)
	t.Run("combinations", func(t *testing.T) {
		i := uint64(0)
		for _, preset := range domain.RiskPresets {
			for _, aggr := range domain.AggressivenessLevels {
				for _, aut := range domain.AutonomyLevels {
					seed := 10_000 + i
					i++
					t.Run(fmt.Sprintf("%s/%s/%s", preset, aggr, aut), func(t *testing.T) {
						t.Parallel()
						st := &propStyle{preset: preset, aggr: aggr, autonomy: aut}
						stats := runPropScenarioStyled(t, seed, ticks, st)
						want := aggr.Params().EntryProb
						for prob := range st.cfgs {
							if int64(prob*10_000+0.5) != int64(want) {
								t.Fatalf("the agent used entry probability %v, want %d bp (aggressiveness %s)", prob, want, aggr)
							}
						}
						if len(st.cfgs) == 0 {
							t.Fatal("the strategy was never asked")
						}
						mu.Lock()
						total.add(stats)
						entries[aggr] += stats.entries
						mu.Unlock()
					})
				}
			}
		}
	})
	t.Logf("exercised: %+v, entries by aggressiveness %v", total, entries)
	for _, a := range domain.AggressivenessLevels {
		if entries[a] == 0 {
			t.Fatalf("no entry at aggressiveness %s: the combination was not exercised", a)
		}
	}
	if total.shorts == 0 || total.latches == 0 {
		t.Fatalf("shorts or daily latches never exercised: %+v", total)
	}
}

// TestLowAutonomyNeverTradesScannerShares: in the styled scenarios at low autonomy, no order
// is ever stored for the share the scanner added.
func TestLowAutonomyNeverTradesScannerShares(t *testing.T) {
	for seed := range uint64(4) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			st := &propStyle{preset: domain.PresetHigh, aggr: domain.AggressivenessFull, autonomy: domain.AutonomyLow}
			runPropScenarioStyled(t, 20_000+seed, 60, st)
			ae := st.ae
			orders, err := ae.store.Orders().List(ae.ctx, ae.a.ID, 10_000)
			if err != nil {
				t.Fatal(err)
			}
			opens := 0
			for _, o := range orders {
				if o.Kind != domain.OrderOpen {
					continue
				}
				opens++
				if o.Instrument == propScannerShare {
					t.Fatalf("low autonomy traded the scanner's share: %+v", o)
				}
			}
			if opens == 0 {
				t.Fatal("nothing was traded: the property was not exercised")
			}
		})
	}
}

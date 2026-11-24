package exec_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"

	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Facts on the wire: a host's query for a fact, answered in order with the stream's frames, and the facts a target pushes. The SDK
// answers what the target does not provide, the integration tier, an unknown kind, an overload and a function that is too slow; the
// function answers the rest, through Proc.Queries, and pushes what changed with Proc.Fact. See facts_query.go.

// factModes are the capabilities of a target that provides the facts.
func factTarget(fn exec.ExecFunc, cwd, foreground exec.FactMode) exec.Target {
	return ctlTarget(fn, func(c *exec.Capabilities) { c.CwdFact, c.ForegroundFact = cwd, foreground })
}

// queryCollector is a function that hands every Query it is given to asked and answers none.
func queryCollector(asked chan<- exec.Query) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for {
			select {
			case q, ok := <-p.Queries:
				if !ok {
					<-ctx.Done()
					return exec.Exit{}, ctx.Err()
				}
				asked <- q
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	}
}

// drainQueries returns the queries that were handed over and nobody has read, and does not wait for more.
func drainQueries(asked <-chan exec.Query) []exec.Query {
	var out []exec.Query
	for {
		select {
		case q := <-asked:
			out = append(out, q)
		default:
			return out
		}
	}
}

// takeQueries returns the queries that were handed over and nobody has read, which have to be n: a build that hands over fewer fails
// the test where it stands, and does not index past them.
func takeQueries(t *testing.T, asked <-chan exec.Query, n int) []exec.Query {
	t.Helper()
	queries := drainQueries(asked)
	if len(queries) != n {
		t.Fatalf("the function was handed %d queries, want %d", len(queries), n)
	}
	return queries
}

// the facts a host reads

func cwdFact(id uint32, path string, reason execv2.UnknownReason) *execv2.Fact {
	return &execv2.Fact{QueryId: id, Value: &execv2.Fact_Cwd{Cwd: &execv2.Directory{Path: path, Reason: reason}}}
}

func foregroundFact(id uint32, state execv2.ForegroundState, name string, pgid uint32, reason execv2.UnknownReason) *execv2.Fact {
	return &execv2.Fact{QueryId: id, Value: &execv2.Fact_Foreground{Foreground: &execv2.Foreground{State: state, Name: name, Pgid: pgid, Reason: reason}}}
}

// wantFacts holds the facts the host read to the ones it expects, in order.
func wantFacts(t *testing.T, got []*execv2.Fact, want ...*execv2.Fact) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%d facts %v, want %d: %v", len(got), got, len(want), want)
		return
	}
	for i := range want {
		if !proto.Equal(got[i], want[i]) {
			t.Errorf("fact %d is %v, want %v", i, got[i], want[i])
		}
	}
}

// violationsOf reads the count of contract violations of a kind, from the process's diagnostics.
func violationsOf(reason string) float64 {
	metrics, _ := sdkdiag.Default().Snapshot()
	for _, m := range metrics {
		if m.GetName() != metricViolations {
			continue
		}
		for _, p := range m.GetPoints() {
			if attrsOf(p)["reason"] == reason {
				return p.GetValue()
			}
		}
	}
	return 0
}

const (
	unsupported = execv2.UnknownReason_UNKNOWN_REASON_UNSUPPORTED
	queryFailed = execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED
	noReason    = execv2.UnknownReason_UNKNOWN_REASON_UNSPECIFIED
)

// Proc.Queries is nil unless the target's capabilities declare a fact that is PULL or PUSH, and Proc.Fact is never nil.
func TestProcQueriesIsNilUnlessTheTargetProvidesAFactAndProcFactIsNeverNil(t *testing.T) {
	cases := []struct {
		name          string
		cwd, fg       exec.FactMode
		wantQueriesOK bool
	}{
		{"no fact", exec.FactModeNone, exec.FactModeNone, false},
		{"the directory is pulled", exec.FactModePull, exec.FactModeNone, true},
		{"the directory is pushed", exec.FactModePush, exec.FactModeNone, true},
		{"the foreground is pulled", exec.FactModeNone, exec.FactModePull, true},
		{"the foreground is pushed", exec.FactModeNone, exec.FactModePush, true},
		{"both", exec.FactModePull, exec.FactModePush, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				type seen struct{ queries, fact bool }
				got := make(chan seen, 1)
				rig := newMemoryRig(t, factTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
					got <- seen{queries: p.Queries != nil, fact: p.Fact != nil}
					<-ctx.Done()
					return exec.Exit{}, ctx.Err()
				}, tc.cwd, tc.fg))
				_ = rig.ctlPlainSession("s1")
				synctest.Wait()
				if s := <-got; s.queries != tc.wantQueriesOK || !s.fact {
					t.Errorf("Proc.Queries non-nil %v and Proc.Fact non-nil %v, want %v and true", s.queries, s.fact, tc.wantQueriesOK)
				}
			})
		})
	}
}

// A query for a fact the target provides reaches the function with its kind, and what the function answers reaches the host as a Fact
// frame that carries the host's id, in the order the answers were given; a fact the function says is unknown carries the reason.
func TestAQueryForAFactTheTargetProvidesReachesTheFunctionAndItsAnswerReachesTheHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 16)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModePush))
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlQuery(7, execv2.FactKind_FACT_KIND_CWD), ctlQuery(8, execv2.FactKind_FACT_KIND_FOREGROUND), ctlQuery(9, execv2.FactKind_FACT_KIND_CWD))
		synctest.Wait()
		queries := drainQueries(asked)
		if len(queries) != 3 || queries[0].Kind != exec.FactKindCwd || queries[1].Kind != exec.FactKindForeground || queries[2].Kind != exec.FactKindCwd {
			t.Fatalf("the function was handed %+v, want a directory, a foreground and a directory query, in order", queries)
		}
		if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
			t.Fatalf("%d facts before the function answered: %v", len(facts), facts)
		}
		queries[1].Answer(exec.Fact{Foreground: exec.Foreground{State: exec.ForegroundBusy, Name: "vim", PGID: 4242}}) // the kind is the query's
		queries[0].Answer(exec.Fact{Kind: exec.FactKindCwd, Cwd: "/srv/app"})
		queries[2].Answer(exec.Fact{Reason: exec.UnknownReasonRemote}) // an empty directory is unknown, and says why
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()),
			foregroundFact(8, execv2.ForegroundState_FOREGROUND_STATE_BUSY, "vim", 4242, noReason),
			cwdFact(7, "/srv/app", noReason),
			cwdFact(9, "", execv2.UnknownReason_UNKNOWN_REASON_REMOTE),
		)
	})
}

// An idle foreground carries no name and no process group, whatever the function said, and an unknown one says why.
func TestAnIdleForegroundCarriesNoNameAndAnUnknownOneCarriesItsReason(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 16)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModeNone, exec.FactModePull))
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlQuery(1, execv2.FactKind_FACT_KIND_FOREGROUND), ctlQuery(2, execv2.FactKind_FACT_KIND_FOREGROUND), ctlQuery(3, execv2.FactKind_FACT_KIND_FOREGROUND))
		synctest.Wait()
		queries := takeQueries(t, asked, 3)
		queries[0].Answer(exec.Fact{Foreground: exec.Foreground{State: exec.ForegroundIdle, Name: "bash", PGID: 7}})
		queries[1].Answer(exec.Fact{Reason: exec.UnknownReasonQueryFailed})
		queries[2].Answer(exec.Fact{Foreground: exec.Foreground{State: exec.ForegroundBusy}}) // busy with no name and no group known
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()),
			foregroundFact(1, execv2.ForegroundState_FOREGROUND_STATE_IDLE, "", 0, noReason),
			foregroundFact(2, execv2.ForegroundState_FOREGROUND_STATE_UNSPECIFIED, "", 0, queryFailed),
			foregroundFact(3, execv2.ForegroundState_FOREGROUND_STATE_BUSY, "", 0, noReason),
		)
	})
}

// A query for a fact the target does not provide is answered by the SDK, unknown and unsupported, at once, with no time passing, and
// never reaches the function: not for a kind whose mode is none, and not for a session that narrowed it to none.
func TestAQueryForAFactTheTargetDoesNotProvideIsAnsweredUnsupportedAtOnceAndNeverReachesTheFunction(t *testing.T) {
	t.Run("a target that provides one of the two", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			asked := make(chan exec.Query, 16)
			rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModeNone, exec.FactModePull))
			host := rig.ctlPlainSession("s1")
			host.ctlSend(ctlQuery(1, execv2.FactKind_FACT_KIND_CWD), ctlQuery(2, execv2.FactKind_FACT_KIND_FOREGROUND))
			synctest.Wait()
			wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(1, "", unsupported))
			if queries := drainQueries(asked); len(queries) != 1 || queries[0].Kind != exec.FactKindForeground {
				t.Errorf("the function was handed %+v, want the one query for the fact the target provides", queries)
			}
		})
	})
	t.Run("a target that provides none", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			asked := make(chan exec.Query, 16)
			rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModeNone, exec.FactModeNone))
			host := rig.ctlPlainSession("s1")
			host.ctlSend(ctlQuery(1, execv2.FactKind_FACT_KIND_CWD), ctlQuery(2, execv2.FactKind_FACT_KIND_FOREGROUND))
			synctest.Wait()
			wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(1, "", unsupported), foregroundFact(2, execv2.ForegroundState_FOREGROUND_STATE_UNSPECIFIED, "", 0, unsupported))
			if queries := drainQueries(asked); len(queries) != 0 {
				t.Errorf("the function was handed %+v: a fact the target does not provide is the SDK's to answer", queries)
			}
		})
	})
	t.Run("a session that narrowed the fact away", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			asked := make(chan exec.Query, 16)
			rig := newMemoryRig(t, factTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
				narrowed := p.Capabilities
				narrowed.CwdFact = exec.FactModeNone
				p.Started(exec.SessionFacts{Capabilities: &narrowed})
				return queryCollector(asked)(ctx, p)
			}, exec.FactModePull, exec.FactModePull))
			host := rig.ctlPlainSession("s1")
			synctest.Wait()
			host.ctlSend(ctlQuery(1, execv2.FactKind_FACT_KIND_CWD), ctlQuery(2, execv2.FactKind_FACT_KIND_FOREGROUND))
			synctest.Wait()
			wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(1, "", unsupported))
			if queries := drainQueries(asked); len(queries) != 1 || queries[0].Kind != exec.FactKindForeground {
				t.Errorf("the function was handed %+v, want the foreground query only", queries)
			}
		})
	})
}

// The integration tier is the SDK's own fact: the answer is unspecified (not observed), it comes at once, and it never reaches the
// function, whatever the target provides, and for a target that provides nothing.
func TestAQueryForTheIntegrationTierIsAnsweredByTheSDKAndNeverReachesTheFunction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 16)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePush, exec.FactModePull))
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlQuery(3, execv2.FactKind_FACT_KIND_INTEGRATION_TIER))
		synctest.Wait()
		want := &execv2.Fact{QueryId: 3, Value: &execv2.Fact_Tier{Tier: execv2.IntegrationTier_INTEGRATION_TIER_UNSPECIFIED}}
		wantFacts(t, ctlFacts(host.ctlFrames()), want)
		if queries := drainQueries(asked); len(queries) != 0 {
			t.Errorf("the function was handed %+v for the integration tier", queries)
		}
	})
}

// A query with the id zero, and one that asks for nothing, are malformed: the stream ends INVALID_REQUEST and the session with it.
func TestAQueryWithIdZeroOrAnUnspecifiedKindEndsTheStreamInvalidRequestAndTheSession(t *testing.T) {
	cases := []struct {
		name string
		q    *execv2.ClientFrame
	}{
		{"id zero", ctlQuery(0, execv2.FactKind_FACT_KIND_CWD)},
		{"an unspecified kind", ctlQuery(5, execv2.FactKind_FACT_KIND_UNSPECIFIED)},
		{"neither", ctlQuery(0, execv2.FactKind_FACT_KIND_UNSPECIFIED)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cancelled := make(chan struct{})
				rig := newMemoryRig(t, factTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
					<-ctx.Done()
					close(cancelled)
					return exec.Exit{}, ctx.Err()
				}, exec.FactModePull, exec.FactModePull))
				host := rig.ctlPlainSession("s1")
				host.ctlSend(tc.q)
				synctest.Wait()
				returned, err := host.ctlStatus()
				if !returned {
					t.Fatal("the stream did not end for a malformed query")
				}
				if class, _ := ctlClass(err); class != "INVALID_REQUEST" {
					t.Errorf("the stream ended with %v (class %q), want INVALID_REQUEST", err, class)
				}
				select {
				case <-cancelled:
				default:
					t.Error("the session did not end with its stream")
				}
				if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
					t.Errorf("a fact was sent for a malformed query: %v", facts)
				}
			})
		})
	}
}

// A kind this build does not know is answered at once with a fact that has no value, which a reader reads as a fact it does not know:
// never silence, and never a guess.
func TestAQueryOfAKindThisBuildDoesNotKnowIsAnsweredWithAnEmptyFact(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 16)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModePull))
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlQuery(4, execv2.FactKind(99)))
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()), &execv2.Fact{QueryId: 4})
		if queries := drainQueries(asked); len(queries) != 0 {
			t.Errorf("the function was handed %+v for a kind this build does not know", queries)
		}
		if returned, err := host.ctlStatus(); returned {
			t.Errorf("the stream ended (%v) for a kind a newer host may ask: frames are lenient", err)
		}
	})
}

// A function that does not answer is answered for: after the deadline, not before, the host gets unknown and QUERY_FAILED, and what
// the function answers after that is ignored and changes nothing. The deadline is the engine's, a field a test shortens.
func TestAnUnansweredQueryIsAnsweredUnknownQueryFailedAtTheDeadlineAndALateAnswerIsIgnored(t *testing.T) {
	for _, deadline := range []time.Duration{0, 2 * time.Second} {
		name := "the default deadline"
		if deadline != 0 {
			name = "a deadline the engine was given"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				want := 5 * time.Second
				asked := make(chan exec.Query, 16)
				rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModeNone))
				if deadline != 0 {
					exec.SetQueryDeadline(rig.plugin, deadline)
					want = deadline
				}
				before := violationsOf("query_unanswered")
				host := rig.ctlPlainSession("s1")
				host.ctlSend(ctlQuery(7, execv2.FactKind_FACT_KIND_CWD))
				synctest.Wait()
				time.Sleep(want - time.Millisecond)
				synctest.Wait()
				if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
					t.Fatalf("%d facts before the deadline of %v: %v", len(facts), want, facts)
				}
				time.Sleep(2 * time.Millisecond)
				synctest.Wait()
				wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(7, "", queryFailed))
				if got := violationsOf("query_unanswered") - before; got != 1 {
					t.Errorf("%v violations counted for a function that did not answer, want 1", got)
				}

				queries := takeQueries(t, asked, 1)
				queries[0].Answer(exec.Fact{Cwd: "/too/late"})
				synctest.Wait()
				time.Sleep(time.Minute)
				synctest.Wait()
				if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
					t.Errorf("a second answer was sent for one query: %v", facts)
				}
			})
		})
	}
}

// A query is answered at most once: the function's first answer stands, a second one is ignored and counted as the author's bug, and the
// deadline that follows an answer answers nothing.
func TestAQueryIsAnsweredAtMostOnceAndTheDeadlineAfterAnAnswerAnswersNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 16)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModeNone))
		before := violationsOf("query_twice")
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlQuery(7, execv2.FactKind_FACT_KIND_CWD))
		synctest.Wait()
		q := takeQueries(t, asked, 1)[0]
		q.Answer(exec.Fact{Cwd: "/first"})
		q.Answer(exec.Fact{Cwd: "/second"})
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(7, "/first", noReason))
		if got := violationsOf("query_twice") - before; got != 1 {
			t.Errorf("%v violations counted for an answer given twice, want 1", got)
		}
		time.Sleep(time.Minute) // far past the deadline
		synctest.Wait()
		if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
			t.Errorf("the deadline of an answered query sent %v", facts)
		}
	})
}

// At most eight queries are waiting for the function at once: the ninth and the rest are answered unknown and QUERY_FAILED at once,
// and a place is free again when a query is answered.
func TestMoreThanEightQueriesWaitingForTheFunctionAreAnsweredQueryFailedAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 64)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModeNone))
		host := rig.ctlPlainSession("s1")
		for id := uint32(1); id <= 12; id++ {
			host.ctlSend(ctlQuery(id, execv2.FactKind_FACT_KIND_CWD))
		}
		synctest.Wait()
		var failed []*execv2.Fact
		for id := uint32(9); id <= 12; id++ {
			failed = append(failed, cwdFact(id, "", queryFailed))
		}
		wantFacts(t, ctlFacts(host.ctlFrames()), failed...)
		queries := drainQueries(asked)
		if len(queries) != 8 {
			t.Fatalf("the function was handed %d queries, want the first 8", len(queries))
		}
		for i, q := range queries {
			q.Answer(exec.Fact{Cwd: fmt.Sprintf("/d%d", i+1)})
		}
		synctest.Wait()
		var answered []*execv2.Fact
		for id := uint32(1); id <= 8; id++ {
			answered = append(answered, cwdFact(id, fmt.Sprintf("/d%d", id), noReason))
		}
		wantFacts(t, ctlFacts(host.ctlFrames()), answered...)

		host.ctlSend(ctlQuery(13, execv2.FactKind_FACT_KIND_CWD), ctlQuery(14, execv2.FactKind_FACT_KIND_CWD))
		synctest.Wait()
		if got := drainQueries(asked); len(got) != 2 {
			t.Errorf("the function was handed %d of the two queries that came once its places were free, want 2", len(got))
		}
		if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
			t.Errorf("%d facts for queries that wait for the function: %v", len(facts), facts)
		}
	})
}

// A function that answers a place free at the deadline: the places of the queries that were not answered in time are free after it,
// and the function is handed more.
func TestThePlacesOfQueriesThatMissedTheirDeadlineAreFreeAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 64)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModeNone))
		host := rig.ctlPlainSession("s1")
		for id := uint32(1); id <= 8; id++ {
			host.ctlSend(ctlQuery(id, execv2.FactKind_FACT_KIND_CWD))
		}
		synctest.Wait()
		_ = drainQueries(asked)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if facts := ctlFacts(host.ctlFrames()); len(facts) != 8 {
			t.Fatalf("%d facts at the deadline of 8 queries, want 8", len(facts))
		}
		host.ctlSend(ctlQuery(9, execv2.FactKind_FACT_KIND_CWD))
		synctest.Wait()
		if got := drainQueries(asked); len(got) != 1 {
			t.Errorf("the function was handed %d queries after the others had missed their deadline, want 1", len(got))
		}
	})
}

// A host that asks more than it reads gets its answers when it reads: the receiver stops reading its frames while the stream holds as
// many unsent answers as it may (a host that does not read waits, as one that does not take its input does), and every query is then
// answered exactly once, in order, and none is lost.
func TestAStormOfQueriesIsAnsweredOnceEachAndTheReceiverWaitsForAHostThatDoesNotRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const storm = 500
		rig := newMemoryRig(t, factTarget(blockUntilCancelled, exec.FactModeNone, exec.FactModeNone))
		host := rig.ctlStreamSized(rig.ctlOpenFrame("s1", false, 0, 0), 1) // a host whose buffer holds one frame
		host.opened(t)
		asked := uint32(0)
		ask := func() bool {
			if asked >= storm {
				return false
			}
			select {
			case host.in <- ctlQuery(asked+1, execv2.FactKind_FACT_KIND_CWD):
				asked++
				return true
			default:
				return false
			}
		}
		for asked < storm {
			synctest.Wait()
			if !ask() {
				break // the host's buffer is full: the receiver has stopped reading
			}
		}
		synctest.Wait()
		// The host has read no answer. What the stream took is what it holds: a bounded number, a long way below the storm.
		if asked >= storm/2 {
			t.Fatalf("the stream took %d of %d queries from a host that reads no answer: it holds them without bound", asked, storm)
		}
		var got []*execv2.Fact
		read := func() {
			for {
				select {
				case f := <-host.out:
					if fact := f.GetFact(); fact != nil {
						got = append(got, fact)
					}
				default:
					return
				}
			}
		}
		for asked < storm {
			read()
			synctest.Wait()
			for ask() {
			}
		}
		for { // the host reads on until the plugin has nothing more to send
			before := len(got)
			read()
			synctest.Wait()
			if read(); len(got) == before {
				break
			}
		}
		if len(got) != storm {
			t.Fatalf("%d answers for %d queries", len(got), storm)
		}
		for i, fact := range got {
			if !proto.Equal(fact, cwdFact(uint32(i+1), "", unsupported)) {
				t.Fatalf("answer %d is %v, want the answer to query %d, unknown and unsupported", i, fact, i+1)
			}
		}
	})
}

// A session that ends with queries waiting leaves nothing behind (the bubble would find a goroutine that did not return), and an
// answer that comes after the end is harmless.
func TestASessionThatEndsWithQueriesWaitingLeavesNothingAndALateAnswerIsHarmless(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 16)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModePull))
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlQuery(1, execv2.FactKind_FACT_KIND_CWD), ctlQuery(2, execv2.FactKind_FACT_KIND_FOREGROUND), ctlQuery(3, execv2.FactKind_FACT_KIND_CWD))
		synctest.Wait()
		queries := drainQueries(asked)
		host.ctlSend(ctlClose(execv2.CloseMode_CLOSE_MODE_KILL))
		synctest.Wait()
		if ended := ctlEnded(host.ctlFrames()); ended == nil {
			t.Fatal("the session did not end")
		}
		for _, q := range queries {
			q.Answer(exec.Fact{Cwd: "/after"}) // the session is over
		}
		synctest.Wait()
	})
}

// What the function answers is held to what the wire can carry: a directory that is not plain text, or is too long, a name with a
// control character, a state that is none of the three, and an answer of another kind than the one asked. What is wrong is dropped and
// never cut, the fact is unknown (QUERY_FAILED) where it cannot be told, and each is counted as the author's bug.
func TestAnAnswerTheWireCannotCarryIsDroppedAndCountedAndTheHostStillGetsAnAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := make(chan exec.Query, 16)
		rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModePull))
		before := violationsOf("fact_invalid")
		host := rig.ctlPlainSession("s1")
		host.ctlSend(
			ctlQuery(1, execv2.FactKind_FACT_KIND_CWD), ctlQuery(2, execv2.FactKind_FACT_KIND_CWD), ctlQuery(3, execv2.FactKind_FACT_KIND_FOREGROUND),
			ctlQuery(4, execv2.FactKind_FACT_KIND_FOREGROUND), ctlQuery(5, execv2.FactKind_FACT_KIND_CWD),
		)
		synctest.Wait()
		queries := takeQueries(t, asked, 5)
		queries[0].Answer(exec.Fact{Cwd: "/tmp/with\nnewline"})
		queries[1].Answer(exec.Fact{Cwd: "/" + strings.Repeat("x", 4096)})
		queries[2].Answer(exec.Fact{Foreground: exec.Foreground{State: exec.ForegroundBusy, Name: "bad\x00name", PGID: 9}})
		queries[3].Answer(exec.Fact{Foreground: exec.Foreground{State: exec.ForegroundState(99)}})
		queries[4].Answer(exec.Fact{Kind: exec.FactKindForeground, Foreground: exec.Foreground{State: exec.ForegroundIdle}}) // a foreground fact for a directory query
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()),
			cwdFact(1, "", queryFailed),
			cwdFact(2, "", queryFailed),
			foregroundFact(3, execv2.ForegroundState_FOREGROUND_STATE_BUSY, "", 9, noReason),
			foregroundFact(4, execv2.ForegroundState_FOREGROUND_STATE_UNSPECIFIED, "", 0, queryFailed),
			cwdFact(5, "", queryFailed),
		)
		if got := violationsOf("fact_invalid") - before; got != 1 {
			t.Errorf("%v violations of kind fact_invalid, want 1: a kind is counted once for a session", got)
		}
	})
}

// A pushed fact is sent with the query id zero. The first value of a kind is sent at once; a value the host already has is not sent
// again; and the newest value of a kind is sent at most once in 100 ms, so that a value that changes faster than that costs the host
// the newest, which is the only one it can act on, and never an older one after it.
func TestAPushedFactIsSentWithQueryIdZeroCoalescedAndRateBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pushes := make(chan exec.Fact, 16)
		rig := newMemoryRig(t, factTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			for {
				select {
				case f := <-pushes:
					p.Fact(f)
				case <-ctx.Done():
					return exec.Exit{}, ctx.Err()
				}
			}
		}, exec.FactModePush, exec.FactModePush))
		host := rig.ctlPlainSession("s1")
		push := func(cwd string) {
			pushes <- exec.Fact{Kind: exec.FactKindCwd, Cwd: cwd}
			synctest.Wait()
		}

		push("/a") // the first value is sent at once
		wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(0, "/a", noReason))
		push("/a") // the host has it
		push("/b") // too soon after the last
		if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
			t.Fatalf("%d facts for a repeat and for a value inside the interval: %v", len(facts), facts)
		}
		time.Sleep(99 * time.Millisecond)
		synctest.Wait()
		if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
			t.Fatalf("a pushed fact was sent after 99 ms: %v", facts)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(0, "/b", noReason))

		// A storm inside one interval costs one frame, with the newest value.
		push("/c")
		push("/d")
		push("/e")
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(0, "/e", noReason))

		// A value that goes back to what the host has before its turn comes is never sent.
		push("/f")
		push("/e")
		time.Sleep(time.Second)
		synctest.Wait()
		if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
			t.Errorf("a value the host already had was sent: %v", facts)
		}

		// The kinds have their own turns.
		push("/g")
		pushes <- exec.Fact{Kind: exec.FactKindForeground, Foreground: exec.Foreground{State: exec.ForegroundBusy, Name: "vim"}}
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()),
			cwdFact(0, "/g", noReason),
			foregroundFact(0, execv2.ForegroundState_FOREGROUND_STATE_BUSY, "vim", 0, noReason),
		)
	})
}

// A fact is pushed only for a kind whose mode is PUSH: a kind that is PULL, one that is none, and one that is no kind are ignored,
// counted as the author's bug, and send nothing; a PUSH kind beside them is sent.
func TestAPushForAKindThatIsNotPushIsIgnoredAndCounted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, factTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Fact(exec.Fact{Kind: exec.FactKindCwd, Cwd: "/pull"})                                                   // PULL
			p.Fact(exec.Fact{Kind: exec.FactKindForeground, Foreground: exec.Foreground{State: exec.ForegroundIdle}}) // none
			p.Fact(exec.Fact{Cwd: "/no/kind"})
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}, exec.FactModePull, exec.FactModeNone))
		undeclared, invalid := violationsOf("fact_undeclared"), violationsOf("fact_invalid")
		host := rig.ctlPlainSession("s1")
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if facts := ctlFacts(host.ctlFrames()); len(facts) != 0 {
			t.Errorf("facts were pushed for kinds that are not PUSH: %v", facts)
		}
		if got := violationsOf("fact_undeclared") - undeclared; got != 1 {
			t.Errorf("%v violations of kind fact_undeclared, want 1: a kind is counted once for a session", got)
		}
		if got := violationsOf("fact_invalid") - invalid; got != 1 {
			t.Errorf("%v violations of kind fact_invalid for a push with no kind, want 1", got)
		}
	})
}

// A session that narrowed a kind from PUSH to PULL (Started) is held to it: a push of that kind is ignored from then on, and the kind
// that is still PUSH is sent.
func TestAPushAfterTheSessionNarrowedTheKindToPullIsIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, factTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			narrowed := p.Capabilities
			narrowed.ForegroundFact = exec.FactModePull
			p.Started(exec.SessionFacts{Capabilities: &narrowed})
			p.Fact(exec.Fact{Kind: exec.FactKindForeground, Foreground: exec.Foreground{State: exec.ForegroundIdle}})
			p.Fact(exec.Fact{Kind: exec.FactKindCwd, Cwd: "/ok"})
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}, exec.FactModePush, exec.FactModePush))
		host := rig.ctlPlainSession("s1")
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(0, "/ok", noReason))
	})
}

// A function that pushes in its first moments, before the stream that serves it has begun to receive, is not lost: the newest value of
// each kind is kept for the stream.
func TestAFactPushedBeforeTheStreamBeganIsSentWhenItDoes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, factTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Fact(exec.Fact{Kind: exec.FactKindCwd, Cwd: "/home"})
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}, exec.FactModePush, exec.FactModeNone))
		host := rig.ctlPlainSession("s1")
		synctest.Wait()
		wantFacts(t, ctlFacts(host.ctlFrames()), cwdFact(0, "/home", noReason))
	})
}

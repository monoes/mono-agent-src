package agent

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

func TestTurnPrinter(t *testing.T) {
	t.Parallel()
	say := func(text string) monomind.Event { return monomind.Event{Type: monomind.EventAssistant, Text: text} }
	done := monomind.Event{Type: monomind.EventDone}
	cases := []struct {
		name   string
		events []monomind.Event
		want   string
	}{
		{
			"streamed chunks print as running text",
			[]monomind.Event{{Type: monomind.EventStart, StreamsIncrementally: true}, say("o"), say("k"), done},
			"ok\nexit: 0\n",
		},
		{
			"a tool call ends the streamed line",
			[]monomind.Event{{Type: monomind.EventStart, StreamsIncrementally: true}, say("Checking."), {Type: monomind.EventToolCall}, say("Done.\n"), done},
			"Checking.\nDone.\nexit: 0\n",
		},
		{
			"whole messages print one per line",
			[]monomind.Event{{Type: monomind.EventStart}, say("first"), say("second"), done},
			"first\nsecond\nexit: 0\n",
		},
		{
			"an error after streamed text starts a new line",
			[]monomind.Event{{Type: monomind.EventStart, StreamsIncrementally: true}, say("partial"), {Type: monomind.EventError, Code: "timeout", ErrMessage: "turn timed out"}, {Type: monomind.EventDone, ExitCode: 1}},
			"partial\nerror: timeout: turn timed out\nexit: 1\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			p := &turnPrinter{w: &out}
			for _, ev := range tc.events {
				p.print(ev)
			}
			if out.String() != tc.want {
				t.Fatalf("output = %q, want %q", out.String(), tc.want)
			}
		})
	}
}

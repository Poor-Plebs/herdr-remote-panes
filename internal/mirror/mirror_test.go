package mirror

import (
	"errors"
	"testing"
)

func TestShouldReportFailure(t *testing.T) {
	boom := errors.New("ssh: connection closed")

	tests := []struct {
		name        string
		err         error
		askedToStop bool
		want        bool
	}{
		{
			// The connection dropped on its own. Without recording it, the
			// machine's space empties and nothing brings it back.
			name: "a bridge that dies on its own is a failure",
			err:  boom, askedToStop: false, want: true,
		},
		{
			// Closing a pane signals this process, and the bridge then exits
			// with an error too. Recording that would reopen the terminal
			// someone just closed.
			name: "an exit that was asked for is not a failure",
			err:  boom, askedToStop: true, want: false,
		},
		{
			name: "a clean exit is never a failure",
			err:  nil, askedToStop: false, want: false,
		},
		{
			name: "a clean exit after being asked to stop is not a failure",
			err:  nil, askedToStop: true, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldReportFailure(tt.err, tt.askedToStop); got != tt.want {
				t.Errorf("shouldReportFailure(%v, %v) = %v, want %v",
					tt.err, tt.askedToStop, got, tt.want)
			}
		})
	}
}

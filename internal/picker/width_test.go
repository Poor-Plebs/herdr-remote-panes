package picker

import "testing"

func TestNameWidthFitsThePopup(t *testing.T) {
	// The menu runs in a popup whose width it does not control.
	for _, cols := range []int{20, 40, 80, 200} {
		if w := nameWidth(cols); w < 8 || w > 40 {
			t.Errorf("nameWidth(%d) = %d, want it clamped between 8 and 40", cols, w)
		}
	}
}

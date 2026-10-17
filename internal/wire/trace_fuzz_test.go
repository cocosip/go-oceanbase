package wire

import "testing"

func FuzzFullTrace(f *testing.F) {
	app, _ := EncodeFullTrace(FullTraceInfo{Module: "module"})
	f.Add(true, encodeTraceExtra(app, true))
	f.Add(false, encodeTraceExtra(app, false))
	f.Add(true, []byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, modern bool, data []byte) {
		if len(data) > maxFullTraceBytes {
			t.Skip()
		}
		_, _ = DecodeFullTrace(data, TraceFeedback{})
		frame := frameIO{trace: &fullTraceState{}}
		_ = frame.decodeTraceExtra(data, modern)
	})
}

package rt

import "testing"

func TestParseSchedField(t *testing.T) {
	text := []byte(`kn (1005, #threads: 4)
-------------------------------------------------------------------
se.sum_exec_runtime                          :         12620.403673
policy                                       :                    6
dl.runtime                                   :              -105913
dl.deadline                                  :          75055464669
`)
	for name, want := range map[string]int64{"dl.deadline": 75055464669, "dl.runtime": -105913, "policy": 6, "dl.period": -1, "dl": -1} {
		if got := parseSchedField(text, name); got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}

package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigPreparationAuthorityClosedShape(t *testing.T) {
	a := ConfigPreparationAuthority{1, "original-grant", "/private/state", 0, 1, 0, 2}
	raw, _ := json.Marshal(a)
	var got ConfigPreparationAuthority
	if e := json.Unmarshal(raw, &got); e != nil || got != a {
		t.Fatal(got, e)
	}
	for _, bad := range []string{`{}`, string(raw[:len(raw)-1]) + `,"extra":1}`, strings.Replace(string(raw), `"state_root":"/private/state"`, `"state_root":null`, 1), strings.Replace(string(raw), `"state_inode":1`, `"state_inode":0`, 1), strings.Replace(string(raw), `"state_device":0`, `"State_device":0`, 1), strings.Replace(string(raw), `"state_device":0`, `"state_device":0,"state_device":0`, 1), strings.Replace(string(raw), `/private/state`, `../state`, 1), strings.Replace(string(raw), `"database_device":0,`, "", 1)} {
		if json.Unmarshal([]byte(bad), &got) == nil {
			t.Fatalf("untrusted authority shape accepted: %s", bad)
		}
	}
}

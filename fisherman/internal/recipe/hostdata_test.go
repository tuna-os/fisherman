package recipe

import "testing"

// copyNetworkProfiles and copyBluetoothPairings are three-valued: absent
// follows live media, true and false are the caller's decision
// (tuna-os/fisherman#411).
func TestCopyHostSecretFields(t *testing.T) {
	tests := []struct {
		json           string
		netSet, netVal bool
		btSet, btVal   bool
	}{
		{`{}`, false, false, false, false},
		{`{"copyNetworkProfiles":null}`, false, false, false, false},
		{`{"copyNetworkProfiles":true,"copyBluetoothPairings":false}`, true, true, true, false},
		{`{"copyBluetoothPairings":true}`, false, false, true, true},
	}
	for _, tt := range tests {
		r, err := Parse([]byte(tt.json))
		if err != nil {
			t.Fatalf("%s: %v", tt.json, err)
		}
		if (r.CopyNetworkProfiles != nil) != tt.netSet || (tt.netSet && *r.CopyNetworkProfiles != tt.netVal) {
			t.Errorf("%s: copyNetworkProfiles = %v", tt.json, r.CopyNetworkProfiles)
		}
		if (r.CopyBluetoothPairings != nil) != tt.btSet || (tt.btSet && *r.CopyBluetoothPairings != tt.btVal) {
			t.Errorf("%s: copyBluetoothPairings = %v", tt.json, r.CopyBluetoothPairings)
		}
	}
	// Not a boolean: refused by the parser, never read as "true".
	if _, err := Parse([]byte(`{"copyNetworkProfiles":"yes"}`)); err == nil {
		t.Error(`"copyNetworkProfiles":"yes" parsed`)
	}
}

func TestFollowLive(t *testing.T) {
	yes, no := true, false
	for _, tt := range []struct {
		field *bool
		live  bool
		want  bool
	}{
		{nil, false, false},
		{nil, true, true},
		{&yes, false, true},
		{&no, true, false},
	} {
		if got := FollowLive(tt.field, tt.live); got != tt.want {
			t.Errorf("FollowLive(%v, %v) = %v, want %v", tt.field, tt.live, got, tt.want)
		}
	}
}

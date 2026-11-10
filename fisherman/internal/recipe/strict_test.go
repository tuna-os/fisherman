package recipe

import (
	"errors"
	"reflect"
	"testing"
)

// The recipe from tuna-os/fisherman#404: a misspelt "encryption" and a field
// from a newer frontend used to parse clean and install unencrypted.
func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`{"disk":"/dev/null","filesystem":"xfs","hostname":"a","image":"x",
		"encrypton":{"type":"luks-passphrase","passphrase":"x"},"locale":"de_DE.UTF-8"}`))
	var p Problem
	if !errors.As(err, &p) {
		t.Fatalf("Parse error = %v, want a Problem", err)
	}
	if p.Code != CodeRecipeUnknownField || p.Field != "encrypton" {
		t.Errorf("problem = %+v, want %s at encrypton", p, CodeRecipeUnknownField)
	}
}

func TestUnknownFieldsPaths(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []string
	}{
		{"none", `{"disk":"/dev/sda","encryption":{"type":"none","passphrase":""}}`, nil},
		{"top level", `{"encrypton":{}}`, []string{"encrypton"}},
		{"nested object", `{"user":{"username":"a","pasword":"x"}}`, []string{"user.pasword"}},
		{"nested in encryption", `{"encryption":{"type":"none","passphase":"x"}}`, []string{"encryption.passphase"}},
		{"array element", `{"customMounts":[{"partition":"/dev/sda1"},{"partition":"/dev/sda2","fs":"xfs"}]}`,
			[]string{"customMounts[1].fs"}},
		{"pointer struct", `{"varDisk":{"disk":"/dev/sdb","keep":true}}`, []string{"varDisk.keep"}},
		{"slurp users", `{"slurp":{"sourcePartition":"/dev/sda3","users":[{"name":"a","cats":[]}]}}`,
			[]string{"slurp.users[0].cats"}},
		{"sorted, all reported", `{"zz":1,"aa":2,"user":{"x":1}}`, []string{"aa", "user.x", "zz"}},
		{"a misspelt extra is not dropped", `{"extras":{"printServices":true,"printService":true}}`,
			[]string{"extras.printService"}},
		// encoding/json matches keys without regard to case, so these are
		// applied, not dropped, and are not "unknown".
		{"case-insensitive like the decoder", `{"Hostname":"a","ENCRYPTION":{"Type":"none"}}`, nil},
		{"null values", `{"varDisk":null,"customMounts":null,"user":null}`, nil},
		{"wrong shapes are the decoder's to report", `{"user":"alice","customMounts":{}}`, nil},
		{"not an object", `[]`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, p := range UnknownFields([]byte(tt.json)) {
				if p.Code != CodeRecipeUnknownField {
					t.Errorf("code = %q", p.Code)
				}
				got = append(got, p.Field)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnknownFields = %v, want %v", got, tt.want)
			}
		})
	}
}

// Every key the five frontends in tuna-os/bootc-installer write, and the
// keys other consumers (CI recipes, wootc-style embedders) use, must be
// known: making the parser strict must not refuse a recipe that was applied
// in full before. Sources, read when this test was written:
//
//	GNOME   bootc_installer/utils/processor.py
//	KDE     frontends/kde/src/recipe.cpp
//	COSMIC  frontends/cosmic/src/model.rs
//	Niri    frontends/niri/installer/main.go, ui/installer.qml
//	XFCE    frontends/xfce/tuna_installer_xfce/core.py
//	schema  shared/recipe/fisherman-recipe.schema.json
func TestFrontendRecipeKeysAreKnown(t *testing.T) {
	recipes := map[string]string{
		"gnome": `{"disk":"/dev/vda","filesystem":"btrfs","btrfsSubvolumes":true,
			"encryption":{"type":"luks-passphrase","passphrase":"p"},
			"image":"i","targetImgref":"t","selinuxDisabled":false,"unifiedStorage":false,
			"composeFsBackend":false,"bootloader":"grub2","hostname":"h","distroID":"d",
			"flatpaks":["a.b.C"],"user":{"username":"u","fullname":"U","password":"$6$s$h","groups":["wheel"]},
			"customMounts":[{"partition":"/dev/vda1","target":"/boot/efi","fstype":"fat32"}],
			"imageType":"bootc","flatpakVarPath":"state/os/default/var",
			"additionalImageStores":["/usr/lib/bootc/storage"],
			"varDisk":{"disk":"/dev/vdb","keepExisting":false}}`,
		"kde, cosmic, niri, xfce": `{"disk":"/dev/vda","filesystem":"xfs","btrfsSubvolumes":false,
			"encryption":{"type":"tpm2-luks-passphrase","passphrase":"p"},
			"image":"i","targetImgref":"t","bootloader":"systemd","composeFsBackend":true,
			"flatpaks":["a.b.C"],"additionalImageStores":[],"distroID":"d","selinuxDisabled":false,
			"hostname":"h","user":{"username":"u","fullname":"U","password":"p","groups":["wheel"]}}`,
		"schema-only and embedder keys": `{"brewTap":"o/n","genericImage":true,"luksMapperName":"m",
			"slurp":{"sourcePartition":"/dev/sda3","users":[{"name":"n","categories":["Documents"]}]},
			"slurpWallpapers":true,"targetMount":"/mnt/t","zfsPoolName":"rpool",
			"copyNetworkProfiles":true,"copyBluetoothPairings":false,
			"extras":{"printServices":true,"oemSetup":true,"audioNames":true,"bootSplash":true},
			"experimental":["zfs"]}`,
	}
	for name, raw := range recipes {
		if ps := UnknownFields([]byte(raw)); len(ps) > 0 {
			t.Errorf("%s: %v", name, ps)
		}
	}
}

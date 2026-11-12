package diagnostics

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// Field policies: what the bundle does with each recipe field. Every json
// field of recipe.Recipe has one (TestRecipeFieldPolicyCoversEveryField),
// and docs/DIAGNOSTICS.md lists them (TestDiagnosticsDocListsRecipeFields).
const (
	// PolicyValue: the value, held to its enum or shape (else "[other]").
	PolicyValue = "value"
	// PolicyIdentifier: the value is a host, user or device identifier,
	// masked unless --include-identifiers.
	PolicyIdentifier = "identifier"
	// PolicyPresence: only whether the field is set, or how many entries
	// it has.
	PolicyPresence = "presence"
	// PolicySecret: never in the bundle, not even its length; only
	// whether it is set.
	PolicySecret = "secret"
)

// RecipeFieldPolicy maps each json field of recipe.Recipe, and of its
// nested objects, to its policy. A field that is not here is not in the
// bundle.
var RecipeFieldPolicy = map[string]string{
	"disk":                     PolicyIdentifier,
	"filesystem":               PolicyValue,
	"btrfsSubvolumes":          PolicyValue,
	"encryption.type":          PolicyValue,
	"encryption.passphrase":    PolicySecret,
	"image":                    PolicyValue,
	"targetImgref":             PolicyValue,
	"sourceReuse":              PolicyPresence,
	"selinuxDisabled":          PolicyValue,
	"unifiedStorage":           PolicyValue,
	"composeFsBackend":         PolicyValue,
	"genericImage":             PolicyValue,
	"zfsPoolName":              PolicyPresence,
	"bootloader":               PolicyValue,
	"imageType":                PolicyValue,
	"flatpakVarPath":           PolicyPresence,
	"hostname":                 PolicyIdentifier,
	"flatpaks":                 PolicyPresence,
	"user.username":            PolicyIdentifier,
	"user.fullname":            PolicyPresence,
	"user.password":            PolicySecret,
	"user.groups":              PolicyValue,
	"customMounts[].partition": PolicyIdentifier,
	"customMounts[].target":    PolicyValue,
	"customMounts[].fstype":    PolicyValue,
	"varDisk.disk":             PolicyIdentifier,
	"varDisk.keepExisting":     PolicyValue,
	"additionalImageStores":    PolicyPresence,
	"slurpWallpapers":          PolicyValue,
	"slurp":                    PolicyPresence,
	"targetMount":              PolicyPresence,
	"luksMapperName":           PolicyPresence,
	"distroID":                 PolicyPresence,
	"brewTap":                  PolicyPresence,
	"override":                 PolicyValue,
	"copyNetworkProfiles":      PolicyValue,
	"copyBluetoothPairings":    PolicyValue,
	"extras":                   PolicyValue,
	"experimental":             PolicyValue,
	"setBootNext":              PolicyValue,
	"locale":                   PolicyValue,
	"keyboard":                 PolicyValue,
	"timezone":                 PolicyValue,
}

// RecipeSummary is the recipe as the bundle carries it: the effective
// options, never the recipe itself.
type RecipeSummary struct {
	Disk            string          `json:"disk,omitempty"`
	Filesystem      string          `json:"filesystem,omitempty"`
	BtrfsSubvolumes bool            `json:"btrfsSubvolumes"`
	Encryption      EncryptionShape `json:"encryption"`
	Image           string          `json:"image,omitempty"`
	TargetImgref    string          `json:"targetImgref,omitempty"`
	SourceReuse     bool            `json:"sourceReuse"`
	SelinuxDisabled bool            `json:"selinuxDisabled"`
	UnifiedStorage  bool            `json:"unifiedStorage"`
	ComposeFs       bool            `json:"composeFsBackend"`
	GenericImage    bool            `json:"genericImage"`
	ZFSPoolName     bool            `json:"zfsPoolName"`
	Bootloader      string          `json:"bootloader,omitempty"`
	ImageType       string          `json:"imageType,omitempty"`
	FlatpakVarPath  bool            `json:"flatpakVarPath"`
	Hostname        string          `json:"hostname,omitempty"`
	// Flatpaks is "absent" (copy the host's), or the number of entries.
	Flatpaks              any          `json:"flatpaks"`
	User                  *UserShape   `json:"user,omitempty"`
	CustomMounts          []MountShape `json:"customMounts,omitempty"`
	VarDisk               *VarDisk     `json:"varDisk,omitempty"`
	AdditionalImageStores any          `json:"additionalImageStores"`
	SlurpWallpapers       bool         `json:"slurpWallpapers"`
	Slurp                 bool         `json:"slurp"`
	TargetMount           bool         `json:"targetMount"`
	LuksMapperName        bool         `json:"luksMapperName"`
	DistroID              bool         `json:"distroID"`
	BrewTap               bool         `json:"brewTap"`
	Override              []string     `json:"override,omitempty"`
	CopyNetworkProfiles   string       `json:"copyNetworkProfiles"`
	CopyBluetoothPairings string       `json:"copyBluetoothPairings"`
	Extras                any          `json:"extras"`
	Experimental          []string     `json:"experimental,omitempty"`
	SetBootNext           string       `json:"setBootNext"`
	Locale                string       `json:"locale,omitempty"`
	Keyboard              *Keyboard    `json:"keyboard,omitempty"`
	Timezone              string       `json:"timezone,omitempty"`
	// UnknownFields is how many keys the Recipe type does not have. Their
	// names and values are not in the bundle.
	UnknownFields int `json:"unknownFields"`
}

// EncryptionShape is encryption without the passphrase.
type EncryptionShape struct {
	Type          string `json:"type"`
	PassphraseSet bool   `json:"passphraseSet"`
}

// UserShape is user without the password.
type UserShape struct {
	Username    string   `json:"username,omitempty"`
	FullnameSet bool     `json:"fullnameSet"`
	PasswordSet bool     `json:"passwordSet"`
	Groups      []string `json:"groups,omitempty"`
}

// MountShape is one customMounts entry.
type MountShape struct {
	Partition string `json:"partition,omitempty"`
	Target    string `json:"target,omitempty"`
	Fstype    string `json:"fstype,omitempty"`
}

// VarDisk is varDisk.
type VarDisk struct {
	Disk         string `json:"disk,omitempty"`
	KeepExisting bool   `json:"keepExisting"`
}

// Keyboard is keyboard, each name held to its XKB shape.
type Keyboard struct {
	Layout  string `json:"layout,omitempty"`
	Variant string `json:"variant,omitempty"`
	Model   string `json:"model,omitempty"`
}

var (
	groupRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	fstypes = []string{"unformatted", "swap", "fat32", "ext3", "ext4", "xfs", "btrfs"}
)

// recipeSecrets returns the secrets a recipe holds, for the Redactor.
// The recipe need not be valid: a value that only decodes as far as its
// own field still counts.
func recipeSecrets(data []byte) []string {
	var loose struct {
		Encryption struct {
			Passphrase any `json:"passphrase"`
		} `json:"encryption"`
		User struct {
			Password any `json:"password"`
		} `json:"user"`
	}
	_ = json.Unmarshal(data, &loose)
	var out []string
	for _, v := range []any{loose.Encryption.Passphrase, loose.User.Password} {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// summarizeRecipe returns the summary of a recipe, or ok false when the
// data is not a recipe object (the bundle then carries nothing of it).
func summarizeRecipe(data []byte, rd *Redactor) (RecipeSummary, bool) {
	var r recipe.Recipe
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &r) != nil {
		return RecipeSummary{}, false
	}
	s := RecipeSummary{
		Disk:            rd.Ident("device", r.Disk),
		Filesystem:      enum(r.Filesystem, recipe.Filesystems...),
		BtrfsSubvolumes: r.BtrfsSubvolumes,
		Encryption: EncryptionShape{
			Type:          encryptionType(r.Encryption.Type),
			PassphraseSet: r.Encryption.Passphrase != "",
		},
		Image:                 imageRef(r.Image),
		TargetImgref:          imageRef(r.TargetImgref),
		SourceReuse:           r.SourceReuse != nil,
		SelinuxDisabled:       r.SelinuxDisabled,
		UnifiedStorage:        r.UnifiedStorage,
		ComposeFs:             r.ComposeFsBackend,
		GenericImage:          r.GenericImage,
		ZFSPoolName:           r.ZFSPoolName != "",
		Bootloader:            enum(r.Bootloader, "grub2", "systemd"),
		ImageType:             enum(r.ImageType, "bootc", "ostree"),
		FlatpakVarPath:        r.FlatpakVarPath != "",
		Hostname:              rd.Ident("host", r.Hostname),
		Flatpaks:              countOrAbsent(r.Flatpaks == nil, len(r.Flatpaks)),
		AdditionalImageStores: countOrAbsent(r.AdditionalImageStores == nil, len(r.AdditionalImageStores)),
		SlurpWallpapers:       r.SlurpWallpapers,
		Slurp:                 r.Slurp != nil,
		TargetMount:           r.TargetMount != "",
		LuksMapperName:        r.LuksMapperName != "",
		DistroID:              r.DistroID != "",
		BrewTap:               r.BrewTap != "",
		CopyNetworkProfiles:   triState(r.CopyNetworkProfiles),
		CopyBluetoothPairings: triState(r.CopyBluetoothPairings),
		SetBootNext:           triState(r.SetBootNext),
		UnknownFields:         len(recipe.UnknownFields(trimmed)),
	}
	for _, o := range r.Override {
		s.Override = append(s.Override, enum(o, recipe.Overrides...))
	}
	for _, x := range r.Experimental {
		s.Experimental = append(s.Experimental, enum(x, recipe.Experimentals...))
	}
	if r.User.Username != "" || r.User.Password != "" || r.User.Fullname != "" || len(r.User.Groups) > 0 {
		u := &UserShape{
			Username:    rd.Ident("user", r.User.Username),
			FullnameSet: r.User.Fullname != "",
			PasswordSet: r.User.Password != "",
		}
		for _, g := range r.User.Groups {
			u.Groups = append(u.Groups, token(g, groupRe))
		}
		s.User = u
	}
	for _, m := range r.CustomMounts {
		ms := MountShape{Partition: rd.Ident("device", m.Partition), Fstype: enum(m.Fstype, fstypes...)}
		if m.Target == "swap" {
			ms.Target = "swap"
		} else {
			ms.Target = token(m.Target, pathRe)
		}
		s.CustomMounts = append(s.CustomMounts, ms)
	}
	if r.VarDisk != nil {
		s.VarDisk = &VarDisk{Disk: rd.Ident("device", r.VarDisk.Disk), KeepExisting: r.VarDisk.KeepExisting}
	}
	if r.Extras == nil {
		s.Extras = "absent"
	} else {
		s.Extras = append([]string{}, r.Extras.Names()...)
	}
	if len(recipe.CheckLocale(r.Locale)) == 0 {
		s.Locale = r.Locale
	} else {
		s.Locale = Other
	}
	if len(recipe.CheckTimezone(r.Timezone)) == 0 {
		s.Timezone = r.Timezone
	} else {
		s.Timezone = Other
	}
	if r.Keyboard != nil {
		if len(recipe.CheckKeyboard(r.Keyboard)) == 0 {
			s.Keyboard = &Keyboard{Layout: r.Keyboard.Layout, Variant: r.Keyboard.Variant, Model: r.Keyboard.Model}
		} else {
			s.Keyboard = &Keyboard{Layout: Other}
		}
	}
	return s, true
}

func encryptionType(t string) string {
	if t == "" {
		return "none"
	}
	if _, ok := recipe.LookupEncryption(t); ok {
		return t
	}
	return Other
}

func countOrAbsent(absent bool, n int) any {
	if absent {
		return "absent"
	}
	return n
}

func triState(b *bool) string {
	switch {
	case b == nil:
		return "absent"
	case *b:
		return "true"
	default:
		return "false"
	}
}

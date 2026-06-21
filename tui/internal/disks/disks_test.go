package disks

import "testing"

func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{int64(1.5 * 1024 * 1024), "1.5 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{int64(500 * 1024 * 1024 * 1024), "500.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
		{int64(2.5 * 1024 * 1024 * 1024 * 1024), "2.5 TB"},
	}

	for _, tc := range tests {
		got := HumanSize(tc.bytes)
		if got != tc.want {
			t.Errorf("HumanSize(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}
